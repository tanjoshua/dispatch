package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"dispatch/agentkit"
)

// testPool creates a throwaway database on the dev Postgres (skipping when it
// is unreachable), applies the real migrations, and tears the database down
// with the test. Tests never touch the dev database's data — the events table
// is append-only there.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	adminURL := os.Getenv("DATABASE_URL")
	if adminURL == "" {
		adminURL = "postgres://dispatch:dispatch@localhost:5432/dispatch?sslmode=disable"
	}
	adminCfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Skipf("DATABASE_URL unparseable: %v", err)
	}
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("postgres unreachable (start docker compose to run store tests): %v", err)
	}

	name := fmt.Sprintf("dispatch_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	})

	cfg := adminCfg.Copy()
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	return pool
}

func testEvent(orgID, runID string, typ agentkit.EventType, dedupeKey string) agentkit.Event {
	return agentkit.Event{
		ID:        agentkit.NewID(),
		OrgID:     orgID,
		RunID:     runID,
		Type:      typ,
		Payload:   json.RawMessage(`{}`),
		DedupeKey: dedupeKey,
	}
}

func newTestRun(t *testing.T, s *Postgres, orgID string) string {
	t.Helper()
	id := agentkit.NewID()
	err := s.CreateRun(context.Background(), agentkit.Run{
		ID: id, OrgID: orgID, Agent: "testagent", Status: agentkit.RunRunning,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return id
}

// TestProposeActionRetryIsIdempotent: a retried proposal (same run + tool-call
// ID, fresh action ID) returns the first stored action and appends exactly one
// action_proposed event.
func TestProposeActionRetryIsIdempotent(t *testing.T) {
	s := NewPostgres(testPool(t))
	ctx := context.Background()
	runID := newTestRun(t, s, "org_test")

	mkAction := func() agentkit.Action {
		return agentkit.Action{
			ID: agentkit.NewID(), OrgID: "org_test", RunID: runID,
			ToolCallID: "tc1", Tool: "act", Input: json.RawMessage(`{"a":1}`),
			State: agentkit.ActionPendingApproval,
		}
	}
	first, err := s.ProposeAction(ctx, mkAction(), testEvent("org_test", runID, agentkit.EventActionProposed, "action_proposed:tc1"))
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	second, err := s.ProposeAction(ctx, mkAction(), testEvent("org_test", runID, agentkit.EventActionProposed, "action_proposed:tc1"))
	if err != nil {
		t.Fatalf("retried propose: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("retry created a second action: %s vs %s", second.ID, first.ID)
	}
	events, _ := s.ListEventsByRun(ctx, "org_test", runID)
	if len(events) != 1 {
		t.Errorf("events = %d, want 1", len(events))
	}
}

// TestRecordDecisionFirstWins: the first decision on an action sticks; a
// racing second decision neither changes state nor appends another event.
func TestRecordDecisionFirstWins(t *testing.T) {
	s := NewPostgres(testPool(t))
	ctx := context.Background()
	runID := newTestRun(t, s, "org_test")

	a, err := s.ProposeAction(ctx, agentkit.Action{
		ID: agentkit.NewID(), OrgID: "org_test", RunID: runID,
		ToolCallID: "tc1", Tool: "act", Input: json.RawMessage(`{}`),
		State: agentkit.ActionPendingApproval,
	}, testEvent("org_test", runID, agentkit.EventActionProposed, "action_proposed:tc1"))
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	approved, err := s.RecordDecision(ctx, a.ID,
		agentkit.Decision{Kind: agentkit.DecisionApprove, DecidedBy: "alice"}, nil,
		testEvent("org_test", runID, agentkit.EventDecisionMade, "decision_made:"+a.ID))
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if approved.State != agentkit.ActionApproved {
		t.Fatalf("state = %s, want approved", approved.State)
	}

	after, err := s.RecordDecision(ctx, a.ID,
		agentkit.Decision{Kind: agentkit.DecisionReject, DecidedBy: "bob", Reason: "no"}, nil,
		testEvent("org_test", runID, agentkit.EventDecisionMade, "decision_made:"+a.ID))
	if err != nil {
		t.Fatalf("second decide: %v", err)
	}
	if after.State != agentkit.ActionApproved || after.Decision.DecidedBy != "alice" {
		t.Errorf("second decision overwrote the first: state=%s decided_by=%s", after.State, after.Decision.DecidedBy)
	}
	events, _ := s.ListEventsByRun(ctx, "org_test", runID)
	if len(events) != 2 { // proposed + one decision
		t.Errorf("events = %d, want 2", len(events))
	}
}

// TestFinishActionRetryKeepsFirstOutcome: a retried FinishAction (crash after
// execute, before ack) must not overwrite the recorded result or duplicate
// the event.
func TestFinishActionRetryKeepsFirstOutcome(t *testing.T) {
	s := NewPostgres(testPool(t))
	ctx := context.Background()
	runID := newTestRun(t, s, "org_test")

	a, err := s.ProposeAction(ctx, agentkit.Action{
		ID: agentkit.NewID(), OrgID: "org_test", RunID: runID,
		ToolCallID: "tc1", Tool: "act", Input: json.RawMessage(`{}`),
		State: agentkit.ActionPendingApproval,
	}, testEvent("org_test", runID, agentkit.EventActionProposed, "action_proposed:tc1"))
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := s.RecordDecision(ctx, a.ID,
		agentkit.Decision{Kind: agentkit.DecisionApprove, DecidedBy: "alice"}, nil,
		testEvent("org_test", runID, agentkit.EventDecisionMade, "decision_made:"+a.ID)); err != nil {
		t.Fatalf("decide: %v", err)
	}

	first, err := s.FinishAction(ctx, a.ID, json.RawMessage(`{"sent":true}`), "",
		testEvent("org_test", runID, agentkit.EventActionExecuted, "action_executed:"+a.ID))
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if first.State != agentkit.ActionCompleted {
		t.Fatalf("state = %s, want completed", first.State)
	}
	retry, err := s.FinishAction(ctx, a.ID, nil, "boom",
		testEvent("org_test", runID, agentkit.EventActionFailed, "action_failed:"+a.ID))
	if err != nil {
		t.Fatalf("retried finish: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(retry.Result, &result); err != nil {
		t.Fatalf("stored result not JSON: %v", err)
	}
	if retry.State != agentkit.ActionCompleted || result["sent"] != true || retry.Error != "" {
		t.Errorf("retry overwrote the outcome: state=%s result=%s err=%q", retry.State, retry.Result, retry.Error)
	}
	events, _ := s.ListEventsByRun(ctx, "org_test", runID)
	if len(events) != 3 { // proposed + decision + one execution outcome
		t.Errorf("events = %d, want 3", len(events))
	}
}

// TestAppendEventDedupes: the (run_id, dedupe_key) constraint makes appends
// idempotent.
func TestAppendEventDedupes(t *testing.T) {
	s := NewPostgres(testPool(t))
	ctx := context.Background()
	runID := newTestRun(t, s, "org_test")

	for i := 0; i < 2; i++ {
		if err := s.AppendEvent(ctx, testEvent("org_test", runID, agentkit.EventLLMCompleted, "llm_completed:1")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	events, _ := s.ListEventsByRun(ctx, "org_test", runID)
	if len(events) != 1 {
		t.Errorf("events = %d, want 1", len(events))
	}
}

// TestReadsAreOrgScoped: by-ID reads with the wrong org behave as not-found.
func TestReadsAreOrgScoped(t *testing.T) {
	s := NewPostgres(testPool(t))
	ctx := context.Background()
	runID := newTestRun(t, s, "org_a")

	a, err := s.ProposeAction(ctx, agentkit.Action{
		ID: agentkit.NewID(), OrgID: "org_a", RunID: runID,
		ToolCallID: "tc1", Tool: "act", Input: json.RawMessage(`{}`),
		State: agentkit.ActionPendingApproval,
	}, testEvent("org_a", runID, agentkit.EventActionProposed, "action_proposed:tc1"))
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	if _, err := s.GetRun(ctx, "org_b", runID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRun cross-org = %v, want ErrNotFound", err)
	}
	if _, err := s.GetAction(ctx, "org_b", a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAction cross-org = %v, want ErrNotFound", err)
	}
	if actions, _ := s.ListActionsByRun(ctx, "org_b", runID); len(actions) != 0 {
		t.Errorf("ListActionsByRun cross-org = %d rows, want 0", len(actions))
	}
	if events, _ := s.ListEventsByRun(ctx, "org_b", runID); len(events) != 0 {
		t.Errorf("ListEventsByRun cross-org = %d rows, want 0", len(events))
	}
	if _, err := s.GetRun(ctx, "org_a", runID); err != nil {
		t.Errorf("GetRun same-org: %v", err)
	}
}
