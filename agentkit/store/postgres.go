package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"dispatch/agentkit"
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

func (s *Postgres) GetRun(ctx context.Context, id string) (*agentkit.Run, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, agent, status, created_at, updated_at
		FROM runs WHERE id = $1`, id)
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
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO actions (id, org_id, run_id, tool_call_id, tool, input, state)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (run_id, tool_call_id) DO NOTHING`,
			action.ID, action.OrgID, action.RunID, action.ToolCallID,
			action.Tool, action.Input, action.State)
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
	case agentkit.DecisionReject:
		state = agentkit.ActionRejected
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE actions
			SET state = $2, decision_kind = $3, decided_by = $4,
			    decision_reason = $5, edited_input = $6, decided_at = now()
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
	return s.GetAction(ctx, actionID)
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
	return s.GetAction(ctx, actionID)
}

func (s *Postgres) GetAction(ctx context.Context, id string) (*agentkit.Action, error) {
	return s.scanAction(s.pool.QueryRow(ctx, actionSelect+` WHERE id = $1`, id))
}

func (s *Postgres) getActionByToolCall(ctx context.Context, runID, toolCallID string) (*agentkit.Action, error) {
	return s.scanAction(s.pool.QueryRow(ctx,
		actionSelect+` WHERE run_id = $1 AND tool_call_id = $2`, runID, toolCallID))
}

func (s *Postgres) ListActionsByRun(ctx context.Context, runID string) ([]agentkit.Action, error) {
	rows, err := s.pool.Query(ctx, actionSelect+` WHERE run_id = $1 ORDER BY proposed_at`, runID)
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

func (s *Postgres) ListEventsByRun(ctx context.Context, runID string) ([]agentkit.Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, run_id, type, payload, dedupe_key, created_at
		FROM events WHERE run_id = $1 ORDER BY created_at, id`, runID)
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
	       proposed_at, decided_at, executed_at
	FROM actions`

func (s *Postgres) scanAction(row pgx.Row) (*agentkit.Action, error) {
	var a agentkit.Action
	var decisionKind, decidedBy, decisionReason, execErr *string
	err := row.Scan(&a.ID, &a.OrgID, &a.RunID, &a.ToolCallID, &a.Tool,
		&a.Input, &a.EditedInput, &a.State,
		&decisionKind, &decidedBy, &decisionReason, &a.Result, &execErr,
		&a.ProposedAt, &a.DecidedAt, &a.ExecutedAt)
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
