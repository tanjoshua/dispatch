package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"dispatch/agentkit"
	"dispatch/agentkit/llm"
)

// ErrNotFound is returned when a run or action does not exist.
var ErrNotFound = errors.New("agentkit/store: not found")

// Postgres implements Store on a pgx connection pool.
type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) CreateRun(ctx context.Context, run agentkit.Run) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runs (id, org_id, agent, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING`,
		run.ID, run.OrgID, run.Agent, run.Status)
	return err
}

func (s *Postgres) GetRun(ctx context.Context, orgID, id string) (*agentkit.Run, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, agent, status, created_at, updated_at
		FROM runs WHERE id = $1 AND org_id = $2`, id, orgID)
	var r agentkit.Run
	if err := row.Scan(&r.ID, &r.OrgID, &r.Agent, &r.Status, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &r, nil
}

func (s *Postgres) FinishRun(ctx context.Context, runID string, status agentkit.RunStatus, event agentkit.Event) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE runs SET status = $2, updated_at = now()
			WHERE id = $1 AND status = 'running'`, runID, status); err != nil {
			return err
		}
		return appendEvent(ctx, tx, event)
	})
}

func (s *Postgres) ProposeAction(ctx context.Context, action agentkit.Action, event agentkit.Event) (*agentkit.Action, error) {
	dependencies := action.DependencyVersions
	if len(dependencies) == 0 {
		dependencies = json.RawMessage(`{}`)
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
				INSERT INTO actions (id, org_id, run_id, tool_call_id, tool, input, state, context_revision, dependency_versions, responds_through_event_seq, model_turn_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
				ON CONFLICT (run_id, tool_call_id) DO NOTHING`,
			action.ID, action.OrgID, action.RunID, action.ToolCallID,
			action.Tool, action.Input, action.State, action.ContextRevision, dependencies, nullInt64(action.RespondsThroughEventSeq), nullIfEmpty(action.ModelTurnID))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // retried proposal; event was appended the first time
		}
		return appendEvent(ctx, tx, event)
	})
	if err != nil {
		return nil, err
	}
	return s.getActionByToolCall(ctx, action.RunID, action.ToolCallID)
}

func (s *Postgres) RecordDecision(ctx context.Context, actionID string, decision agentkit.Decision, editedInput json.RawMessage, event agentkit.Event) (*agentkit.Action, error) {
	state := agentkit.ActionApproved
	switch decision.Kind {
	case agentkit.DecisionApproveWithEdits:
		state = agentkit.ActionApprovedWithEdits
	case agentkit.DecisionReject, agentkit.DecisionDismiss, agentkit.DecisionSupersede:
		state = agentkit.ActionRejected
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE actions
			SET state = $2, decision_kind = $3, decided_by = $4,
			    decision_reason = $5, edited_input = $6, decided_at = now(), version = version + 1
			WHERE id = $1 AND decided_at IS NULL`,
			actionID, state, decision.Kind, decision.DecidedBy, decision.Reason, editedInput)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // already decided; keep the first decision
		}
		return appendEvent(ctx, tx, event)
	})
	if err != nil {
		return nil, err
	}
	return s.GetAction(ctx, event.OrgID, actionID)
}

func (s *Postgres) DecideAction(ctx context.Context, cmd DecisionCommand) (*agentkit.Action, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	var priorAction string
	err = tx.QueryRow(ctx, `SELECT action_id FROM action_commands WHERE org_id=$1 AND id=$2`, cmd.OrgID, cmd.ID).Scan(&priorAction)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		a, e := s.GetAction(ctx, cmd.OrgID, priorAction)
		return a, true, e
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	var version, revision, actionRevision int64
	var state agentkit.ActionState
	var runID string
	err = tx.QueryRow(ctx, `SELECT a.version,a.state,a.run_id,c.context_revision,a.context_revision FROM actions a
		JOIN run_bindings rb ON rb.run_id=a.run_id JOIN conversations c ON c.id=rb.conversation_id
		WHERE a.id=$1 AND a.org_id=$2 FOR UPDATE OF c,a`, cmd.ActionID, cmd.OrgID).Scan(&version, &state, &runID, &revision, &actionRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if state == agentkit.ActionSuperseded || revision != cmd.ExpectedContextRevision || actionRevision != revision {
		return nil, false, ErrStaleAction
	}
	if state != agentkit.ActionPendingApproval {
		return nil, false, ErrAlreadyResolved
	}
	if version != cmd.ExpectedActionVersion {
		return nil, false, ErrVersionConflict
	}
	newState := agentkit.ActionApproved
	if cmd.Decision.Kind == agentkit.DecisionApproveWithEdits {
		newState = agentkit.ActionApprovedWithEdits
	}
	if cmd.Decision.Kind == agentkit.DecisionReject || cmd.Decision.Kind == agentkit.DecisionDismiss {
		newState = agentkit.ActionRejected
	}
	tag, err := tx.Exec(ctx, `UPDATE actions SET state=$2,decision_kind=$3,decided_by=$4,decision_reason=$5,
		edited_input=$6,decided_at=now(),version=version+1 WHERE id=$1 AND version=$7 AND state='pending_approval'`,
		cmd.ActionID, newState, cmd.Decision.Kind, cmd.Decision.DecidedBy, cmd.Decision.Reason, cmd.EditedInput, cmd.ExpectedActionVersion)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 0 {
		return nil, false, ErrVersionConflict
	}
	payload, _ := json.Marshal(map[string]any{"action_id": cmd.ActionID, "kind": cmd.Decision.Kind, "decided_by": cmd.Decision.DecidedBy, "reason": cmd.Decision.Reason})
	if _, err := tx.Exec(ctx, `INSERT INTO events(id,org_id,run_id,type,payload,dedupe_key) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(run_id,dedupe_key) DO NOTHING`, agentkit.NewID(), cmd.OrgID, runID, agentkit.EventDecisionMade, payload, "decision_command:"+cmd.ID); err != nil {
		return nil, false, err
	}
	request, _ := json.Marshal(map[string]any{"kind": cmd.Decision.Kind, "edited_input": cmd.EditedInput, "reason": cmd.Decision.Reason})
	result, _ := json.Marshal(map[string]any{"status": "recorded", "action_id": cmd.ActionID})
	if _, err := tx.Exec(ctx, `INSERT INTO action_commands(id,org_id,action_id,expected_version,expected_context_revision,kind,actor_id,request,result,http_status)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,200)`, cmd.ID, cmd.OrgID, cmd.ActionID, cmd.ExpectedActionVersion, cmd.ExpectedContextRevision, cmd.Decision.Kind, cmd.Decision.DecidedBy, request, result); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	a, e := s.GetAction(ctx, cmd.OrgID, cmd.ActionID)
	return a, false, e
}

func (s *Postgres) FinishAction(ctx context.Context, actionID string, result json.RawMessage, execErr string, event agentkit.Event) (*agentkit.Action, error) {
	state := agentkit.ActionCompleted
	if execErr != "" {
		state = agentkit.ActionFailed
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE actions
			SET state = $2, result = $3, error = $4, executed_at = now()
			WHERE id = $1 AND executed_at IS NULL`,
			actionID, state, result, nullIfEmpty(execErr))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // already finished
		}
		return appendEvent(ctx, tx, event)
	})
	if err != nil {
		return nil, err
	}
	return s.GetAction(ctx, event.OrgID, actionID)
}

func (s *Postgres) GetAction(ctx context.Context, orgID, id string) (*agentkit.Action, error) {
	return s.scanAction(s.pool.QueryRow(ctx, actionSelect+` WHERE id = $1 AND org_id = $2`, id, orgID))
}

func (s *Postgres) getActionByToolCall(ctx context.Context, runID, toolCallID string) (*agentkit.Action, error) {
	return s.scanAction(s.pool.QueryRow(ctx,
		actionSelect+` WHERE run_id = $1 AND tool_call_id = $2`, runID, toolCallID))
}

func (s *Postgres) ListActionsByRun(ctx context.Context, orgID, runID string) ([]agentkit.Action, error) {
	rows, err := s.pool.Query(ctx, actionSelect+` WHERE run_id = $1 AND org_id = $2 ORDER BY proposed_at`, runID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var actions []agentkit.Action
	for rows.Next() {
		a, err := s.scanAction(rows)
		if err != nil {
			return nil, err
		}
		actions = append(actions, *a)
	}
	return actions, rows.Err()
}

func (s *Postgres) AppendEvent(ctx context.Context, event agentkit.Event) error {
	return s.inTx(ctx, func(tx pgx.Tx) error { return appendEvent(ctx, tx, event) })
}

func (s *Postgres) ListEventsByRun(ctx context.Context, orgID, runID string) ([]agentkit.Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, run_id, type, payload, dedupe_key, created_at
		FROM events WHERE run_id = $1 AND org_id = $2 ORDER BY created_at, id`, runID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []agentkit.Event
	for rows.Next() {
		var e agentkit.Event
		if err := rows.Scan(&e.ID, &e.OrgID, &e.RunID, &e.Type, &e.Payload, &e.DedupeKey, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

const actionSelect = `
	SELECT id, org_id, run_id, tool_call_id, tool, input, edited_input, state,
	       decision_kind, decided_by, decision_reason, result, error,
	       proposed_at, decided_at, executed_at, version, context_revision,
		       dependency_versions, COALESCE(responds_through_event_seq,0), COALESCE(model_turn_id,'')
	FROM actions`

func (s *Postgres) scanAction(row pgx.Row) (*agentkit.Action, error) {
	var a agentkit.Action
	var decisionKind, decidedBy, decisionReason, execErr *string
	err := row.Scan(&a.ID, &a.OrgID, &a.RunID, &a.ToolCallID, &a.Tool,
		&a.Input, &a.EditedInput, &a.State,
		&decisionKind, &decidedBy, &decisionReason, &a.Result, &execErr,
		&a.ProposedAt, &a.DecidedAt, &a.ExecutedAt, &a.Version,
		&a.ContextRevision, &a.DependencyVersions, &a.RespondsThroughEventSeq, &a.ModelTurnID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if decisionKind != nil {
		a.Decision = &agentkit.Decision{Kind: agentkit.DecisionKind(*decisionKind)}
		if decidedBy != nil {
			a.Decision.DecidedBy = *decidedBy
		}
		if decisionReason != nil {
			a.Decision.Reason = *decisionReason
		}
	}
	if execErr != nil {
		a.Error = *execErr
	}
	return &a, nil
}

func (s *Postgres) DecisionStats(ctx context.Context, orgID string) ([]agentkit.ToolDecisionStats, error) {
	// Human-latency figures exclude policy auto-decisions (instant by
	// construction) and count only decided actions.
	rows, err := s.pool.Query(ctx, `
		SELECT tool,
		       count(*) AS proposed,
		       count(*) FILTER (WHERE decision_kind = 'approve' AND decided_by = 'policy:auto') AS auto_approved,
		       count(*) FILTER (WHERE decision_kind = 'approve' AND decided_by <> 'policy:auto') AS approved,
		       count(*) FILTER (WHERE decision_kind = 'approve_with_edits') AS approved_with_edits,
		       count(*) FILTER (WHERE decision_kind = 'reject' AND decided_by <> 'policy:auto') AS rejected,
		       count(*) FILTER (WHERE decision_kind = 'dismiss') AS dismissed,
		       count(*) FILTER (WHERE decision_kind = 'supersede') AS superseded,
		       count(*) FILTER (WHERE state = 'pending_approval') AS pending,
		       min(proposed_at) FILTER (WHERE state = 'pending_approval') AS oldest_pending_at,
		       avg(EXTRACT(EPOCH FROM decided_at - proposed_at))
		           FILTER (WHERE decided_at IS NOT NULL AND decided_by <> 'policy:auto') AS avg_decision_seconds,
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM decided_at - proposed_at))
		           FILTER (WHERE decided_at IS NOT NULL AND decided_by <> 'policy:auto') AS median_decision_seconds
		FROM actions
		WHERE org_id = $1
		GROUP BY tool
		ORDER BY tool`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []agentkit.ToolDecisionStats
	for rows.Next() {
		var st agentkit.ToolDecisionStats
		if err := rows.Scan(&st.Tool, &st.Proposed, &st.AutoApproved, &st.Approved,
			&st.ApprovedWithEdits, &st.Rejected, &st.Dismissed, &st.Superseded,
			&st.Pending, &st.OldestPendingAt,
			&st.AvgDecisionSeconds, &st.MedianDecisionSeconds); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Postgres) AppendRunMessages(ctx context.Context, runID, orgID string, baseSeq int, msgs []llm.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		for i, m := range msgs {
			raw, err := json.Marshal(m)
			if err != nil {
				return fmt.Errorf("marshal run message: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO run_messages (run_id, seq, org_id, message)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (run_id, seq) DO NOTHING`,
				runID, baseSeq+i, orgID, raw); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Postgres) ListRunMessages(ctx context.Context, runID string, upTo int) ([]llm.Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT message FROM run_messages
		WHERE run_id = $1 AND seq < $2 ORDER BY seq`, runID, upTo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []llm.Message
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m llm.Message
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("unmarshal run message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Postgres) GetRunMessage(ctx context.Context, runID string, seq int) (*llm.Message, bool, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		SELECT message FROM run_messages WHERE run_id = $1 AND seq = $2`,
		runID, seq).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var m llm.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false, fmt.Errorf("unmarshal run message: %w", err)
	}
	return &m, true, nil
}

// appendEvent inserts one event, ignoring (run_id, dedupe_key) duplicates.
// The events table is append-only; this is the only writer.
func appendEvent(ctx context.Context, tx pgx.Tx, e agentkit.Event) error {
	payload := e.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO events (id, org_id, run_id, type, payload, dedupe_key)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (run_id, dedupe_key) DO NOTHING`,
		e.ID, e.OrgID, e.RunID, e.Type, payload, e.DedupeKey)
	if err != nil {
		return fmt.Errorf("append event %s: %w", e.Type, err)
	}
	return nil
}

func (s *Postgres) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func nullInt64(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}
