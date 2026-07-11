package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"dispatch/agentkit"
	"dispatch/agentkit/llm"
	akstore "dispatch/agentkit/store"
	"dispatch/agentkit/temporalkit"
)

func domainTestPool(t *testing.T, through string) *pgxpool.Pool {
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
	pingCtx, cancelPing := context.WithTimeout(ctx, 3*time.Second)
	defer cancelPing()
	if err := admin.Ping(pingCtx); err != nil {
		admin.Close()
		t.Skipf("postgres unreachable: %v", err)
	}
	name := fmt.Sprintf("dispatch_domain_test_%d", time.Now().UnixNano())
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
		t.Fatalf("find migrations: %v", err)
	}
	sort.Strings(files)
	for _, path := range files {
		if through != "" && filepath.Base(path) > through {
			break
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if _, err := pool.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	return pool
}

func TestAgentBehaviorCommandsAndModelTurnProvenance(t *testing.T) {
	ctx := context.Background()
	pool := domainTestPool(t, "")
	store := NewStore(pool)

	behavior, err := store.GetAgentBehavior(ctx, "org_dev")
	if err != nil {
		t.Fatal(err)
	}
	expected := behavior.Version
	want := AgentBehavior{AgentName: "Ada", Tone: "brief and direct", CustomInstructions: "Use short sentences."}

	type updateResult struct {
		playbook *Playbook
		err      error
	}
	results := make(chan updateResult, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pb, err := store.UpdateAgentBehavior(ctx, "org_dev", expected, want, "cmd-concurrent", "admin-1")
			results <- updateResult{pb, err}
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.err != nil || result.playbook.Version != expected+1 {
			t.Fatalf("concurrent replay = (%+v, %v)", result.playbook, result.err)
		}
	}
	if _, err := store.UpdateAgentBehavior(ctx, "org_dev", expected, AgentBehavior{
		AgentName: "Different", Tone: want.Tone,
	}, "cmd-concurrent", "admin-1"); !errors.Is(err, ErrIdempotencyKeyReuse) {
		t.Fatalf("idempotency key reuse error = %v", err)
	}
	conflict, err := store.UpdateAgentBehavior(ctx, "org_dev", expected, want, "cmd-conflict", "admin-1")
	if !errors.Is(err, ErrVersionConflict) || conflict.Version != expected+1 {
		t.Fatalf("version conflict = (%+v, %v)", conflict, err)
	}
	replayed, err := store.UpdateAgentBehavior(ctx, "org_dev", expected, want, "cmd-conflict", "admin-1")
	if !errors.Is(err, ErrVersionConflict) || replayed.Version != conflict.Version {
		t.Fatalf("conflict replay = (%+v, %v)", replayed, err)
	}
	channel, err := store.CreateChannelConnection(ctx, ChannelConnection{
		OrgID: "org_dev", Kind: "dev", Address: "secondary-dev",
	}, "cmd-channel", "admin-1")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := ulid.ParseStrict(channel.ID); err != nil {
		t.Fatalf("channel ID %q is not a ULID: %v", channel.ID, err)
	}
	channelReplay, err := store.CreateChannelConnection(ctx, ChannelConnection{
		OrgID: "org_dev", Kind: "dev", Address: "secondary-dev",
	}, "cmd-channel", "admin-1")
	if err != nil || channelReplay.ID != channel.ID {
		t.Fatalf("channel replay = (%+v, %v)", channelReplay, err)
	}
	if _, err := store.CreateChannelConnection(ctx, ChannelConnection{
		OrgID: "org_dev", Kind: "dev", Address: "different-dev",
	}, "cmd-channel", "admin-1"); !errors.Is(err, ErrIdempotencyKeyReuse) {
		t.Fatalf("channel command reuse error = %v", err)
	}

	const (
		runID          = "run_snapshot_test"
		customerID     = "customer_snapshot_test"
		identityID     = "identity_snapshot_test"
		conversationID = "conversation_snapshot_test"
		messageID      = "message_snapshot_test"
		turnID         = "turn_snapshot_test"
	)
	seed := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO runs(id,org_id,agent,status) VALUES($1,'org_dev','intake','running')`, []any{runID}},
		{`INSERT INTO customers(id,org_id,name) VALUES($1,'org_dev','Customer')`, []any{customerID}},
		{`INSERT INTO contact_identities(id,org_id,customer_id,channel_kind,address)
			VALUES($1,'org_dev',$2,'dev','snapshot-customer')`, []any{identityID, customerID}},
		{`INSERT INTO conversations(id,org_id,customer_id,channel_id,contact_identity_id,event_seq,context_revision)
			VALUES($1,'org_dev',$2,'chan_dev',$3,1,1)`, []any{conversationID, customerID, identityID}},
		{`INSERT INTO messages(id,org_id,conversation_id,direction,author,body,event_seq)
			VALUES($1,'org_dev',$2,'inbound','customer','A leaking sink',1)`, []any{messageID, conversationID}},
		{`INSERT INTO run_bindings(run_id,org_id,conversation_id,playbook_id,stage)
			VALUES($1,'org_dev',$2,'pb_field_service','triage')`, []any{runID, conversationID}},
	}
	for _, statement := range seed {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed run: %v", err)
		}
	}

	snapshot, err := store.AgentRuntimeSnapshotForRun(ctx, "org_dev", runID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ContextRevision != 1 || snapshot.EventToSeq != 1 || snapshot.Playbook.Version != expected+1 {
		t.Fatalf("runtime snapshot = %+v", snapshot)
	}
	prepared, err := store.PrepareModelTurn(ctx, temporalkit.ModelTurnRecord{
		CandidateID: turnID, RunID: runID, OrgID: "org_dev", Seq: 1, Agent: "intake",
		Request:         llm.CompletionRequest{Model: "test", System: "snapshot", MaxTokens: 32},
		Tags:            map[string]string{"prompt_version": "prompt-test"},
		ContextRevision: snapshot.ContextRevision, EventToSeq: snapshot.EventToSeq,
		TriggeringMessageIDs: []string{messageID}, DependencyVersions: snapshot.DependencyVersions,
	})
	if err != nil || prepared.ID != turnID || prepared.EventToSeq != 1 {
		t.Fatalf("prepare model turn = (%+v, %v)", prepared, err)
	}
	firstResponse := &llm.CompletionResponse{Content: []llm.ContentBlock{{Type: "text", Text: "first"}}, StopReason: llm.StopEndTurn}
	canonical, err := store.CompleteModelTurn(ctx, turnID, firstResponse)
	if err != nil || canonical.Text() != "first" {
		t.Fatalf("complete model turn = (%+v, %v)", canonical, err)
	}
	canonical, err = store.CompleteModelTurn(ctx, turnID, &llm.CompletionResponse{
		Content: []llm.ContentBlock{{Type: "text", Text: "second"}}, StopReason: llm.StopEndTurn,
	})
	if err != nil || canonical.Text() != "first" {
		t.Fatalf("canonical retry response = (%+v, %v)", canonical, err)
	}

	actionStore := akstore.NewPostgres(pool)
	actionID := "action_snapshot_test"
	_, err = actionStore.ProposeAction(ctx, agentkit.Action{
		ID: actionID, OrgID: "org_dev", RunID: runID, ToolCallID: "call-1",
		Tool: "route_intent", Input: json.RawMessage(`{"lane":"inquiry"}`),
		State: agentkit.ActionPendingApproval, ModelTurnID: turnID,
		ContextRevision: snapshot.ContextRevision, DependencyVersions: snapshot.DependencyVersions,
	}, agentkit.Event{
		ID: "event_action_snapshot_test", OrgID: "org_dev", RunID: runID,
		Type: agentkit.EventActionProposed, Payload: json.RawMessage(`{}`), DedupeKey: "propose:call-1",
	})
	if err != nil {
		t.Fatalf("propose action: %v", err)
	}
	if err := store.ValidateActionSourceMessages(ctx, actionID, runID, []string{messageID}); err != nil {
		t.Fatalf("validate visible source: %v", err)
	}
	if err := store.ValidateActionSourceMessages(ctx, actionID, runID, []string{"made-up"}); err == nil {
		t.Fatal("fabricated source message was accepted")
	}
	if err := store.RouteRunToLane(ctx, runID, "inquiry", "question", []string{messageID}, actionID, 1); err != nil {
		t.Fatalf("route current context: %v", err)
	}
	if err := store.RouteRunToLane(ctx, runID, "inquiry", "idempotent retry", []string{messageID}, actionID, 1); err != nil {
		t.Fatalf("route retry: %v", err)
	}
	if err := store.RouteRunToLane(ctx, runID, "inquiry", "stale retry", []string{messageID}, "stale-action", 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale route error = %v", err)
	}

	snapshot, err = store.AgentRuntimeSnapshotForRun(ctx, "org_dev", runID)
	if err != nil {
		t.Fatal(err)
	}
	const createTurnID = "turn_create_test"
	if _, err := store.PrepareModelTurn(ctx, temporalkit.ModelTurnRecord{
		CandidateID: createTurnID, RunID: runID, OrgID: "org_dev", Seq: 2, Agent: "intake",
		Request: llm.CompletionRequest{Model: "test"}, Tags: map[string]string{"prompt_version": "prompt-test"},
		ContextRevision: snapshot.ContextRevision, EventToSeq: snapshot.EventToSeq,
		DependencyVersions: snapshot.DependencyVersions,
	}); err != nil {
		t.Fatal(err)
	}
	const createActionID = "action_create_test"
	if _, err := actionStore.ProposeAction(ctx, agentkit.Action{
		ID: createActionID, OrgID: "org_dev", RunID: runID, ToolCallID: "call-create",
		Tool: "create_case", Input: json.RawMessage(`{"initial_fields":{"issue":"leak"}}`),
		State: agentkit.ActionPendingApproval, ModelTurnID: createTurnID,
		ContextRevision: snapshot.ContextRevision, DependencyVersions: snapshot.DependencyVersions,
	}, agentkit.Event{
		ID: "event_create_test", OrgID: "org_dev", RunID: runID,
		Type: agentkit.EventActionProposed, Payload: json.RawMessage(`{}`), DedupeKey: "propose:call-create",
	}); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateCaseForRun(ctx, runID, json.RawMessage(`{"issue":"leak"}`), []string{messageID}, createActionID, snapshot.ContextRevision)
	if err != nil {
		t.Fatalf("create case: %v", err)
	}
	createdRetry, err := store.CreateCaseForRun(ctx, runID, json.RawMessage(`{"issue":"leak"}`), []string{messageID}, createActionID, snapshot.ContextRevision)
	if err != nil || createdRetry.ID != created.ID {
		t.Fatalf("create case retry = (%+v, %v)", createdRetry, err)
	}

	snapshot, err = store.AgentRuntimeSnapshotForRun(ctx, "org_dev", runID)
	if err != nil {
		t.Fatal(err)
	}
	const updateTurnID = "turn_update_test"
	if _, err := store.PrepareModelTurn(ctx, temporalkit.ModelTurnRecord{
		CandidateID: updateTurnID, RunID: runID, OrgID: "org_dev", Seq: 3, Agent: "intake",
		Request: llm.CompletionRequest{Model: "test"}, Tags: map[string]string{"prompt_version": "prompt-test"},
		ContextRevision: snapshot.ContextRevision, EventToSeq: snapshot.EventToSeq,
		DependencyVersions: snapshot.DependencyVersions,
	}); err != nil {
		t.Fatal(err)
	}
	const updateActionID = "action_update_test"
	if _, err := actionStore.ProposeAction(ctx, agentkit.Action{
		ID: updateActionID, OrgID: "org_dev", RunID: runID, ToolCallID: "call-update",
		Tool: "update_case", Input: json.RawMessage(`{"patch":{"urgency":"high"}}`),
		State: agentkit.ActionPendingApproval, ModelTurnID: updateTurnID,
		ContextRevision: snapshot.ContextRevision, DependencyVersions: snapshot.DependencyVersions,
	}, agentkit.Event{
		ID: "event_update_test", OrgID: "org_dev", RunID: runID,
		Type: agentkit.EventActionProposed, Payload: json.RawMessage(`{}`), DedupeKey: "propose:call-update",
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateCase(ctx, runID, created.ID, created.Version, json.RawMessage(`{"urgency":"high"}`), []string{messageID}, updateActionID, snapshot.ContextRevision)
	if err != nil {
		t.Fatalf("update case: %v", err)
	}
	updatedRetry, err := store.UpdateCase(ctx, runID, created.ID, created.Version, json.RawMessage(`{"urgency":"high"}`), []string{messageID}, updateActionID, snapshot.ContextRevision)
	if err != nil || updatedRetry.Version != updated.Version {
		t.Fatalf("update case retry = (%+v, %v)", updatedRetry, err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES('org_other','Other')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO playbooks(id,org_id,name,pack_id,agent,case_type,config)
		VALUES('pb_other','org_other','Agent Behavior','field-service','intake','field_service_job',
		'{"schema":2,"voice":{"agent_name":"Dispatch","tone":"clear","custom_instructions":""}}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO runs(id,org_id,agent,status) VALUES('run_other','org_other','intake','running')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO actions(id,org_id,run_id,tool_call_id,tool,input,state)
		VALUES('cross_tenant_action','org_dev','run_other','cross-call','route_intent','{}','pending_approval')`); err == nil {
		t.Fatal("cross-tenant action/run relationship was accepted")
	}
}

func TestAgentBehaviorMigrationRejectsLegacyCardinality(t *testing.T) {
	ctx := context.Background()
	pool := domainTestPool(t, "0013_run_stage.sql")
	if _, err := pool.Exec(ctx, `INSERT INTO playbooks(id,org_id,name,agent,case_type,config)
		VALUES('pb_extra','org_dev','Extra','intake','field_service_job','{"schema":1,"voice":{"agent_name":"Extra","tone":"clear"}}')`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0014_agent_behavior.sql"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, string(raw))
	if err == nil || !strings.Contains(err.Error(), "exactly one playbook per organization") {
		t.Fatalf("migration cardinality error = %v", err)
	}
	_ = tx.Rollback(ctx)

	if _, err := pool.Exec(ctx, `DELETE FROM playbooks WHERE id='pb_extra'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES('org_without_playbook','No Behavior')`); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, string(raw))
	if err == nil || !strings.Contains(err.Error(), "exactly one playbook per organization") {
		t.Fatalf("migration missing-playbook error = %v", err)
	}
}
