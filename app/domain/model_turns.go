package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"dispatch/agentkit"
	"dispatch/agentkit/llm"
	"dispatch/agentkit/temporalkit"
)

// ValidateActionSourceMessages verifies that every claimed source is a real
// inbound message from the action's conversation and was present by the end
// of the immutable model-turn snapshot. Future/unseen and cross-tenant IDs are
// rejected rather than written as false provenance.
func (s *Store) ValidateActionSourceMessages(ctx context.Context, actionID, runID string, sourceIDs []string) error {
	if len(sourceIDs) == 0 {
		return errors.New("domain: source_message_ids are required")
	}
	seen := make(map[string]struct{}, len(sourceIDs))
	for _, id := range sourceIDs {
		if id == "" {
			return errors.New("domain: source_message_ids cannot contain an empty ID")
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("domain: duplicate source message %q", id)
		}
		seen[id] = struct{}{}
	}

	var orgID, conversationID string
	var eventToSeq int64
	err := s.pool.QueryRow(ctx, `SELECT a.org_id,cs.conversation_id,cs.event_to_seq
		FROM actions a
		JOIN model_turns mt ON mt.id=a.model_turn_id AND mt.org_id=a.org_id
		JOIN context_snapshots cs ON cs.id=mt.context_snapshot_id AND cs.org_id=mt.org_id
		WHERE a.id=$1 AND a.run_id=$2`, actionID, runID).
		Scan(&orgID, &conversationID, &eventToSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		// Actions proposed before the model-turn cutover still carry the original
		// conversation revision. Preserve those pending proposals by validating
		// against that cursor; new actions must use the immutable snapshot above.
		err = s.pool.QueryRow(ctx, `SELECT a.org_id,rb.conversation_id,a.context_revision
			FROM actions a JOIN run_bindings rb ON rb.run_id=a.run_id AND rb.org_id=a.org_id
			WHERE a.id=$1 AND a.run_id=$2 AND a.model_turn_id IS NULL`, actionID, runID).
			Scan(&orgID, &conversationID, &eventToSeq)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("domain: action has no provenance snapshot")
		}
	}
	if err != nil {
		return err
	}
	var count int
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM messages
		WHERE org_id=$1 AND conversation_id=$2 AND direction='inbound'
		  AND event_seq IS NOT NULL AND event_seq <= $3 AND id=ANY($4)`,
		orgID, conversationID, eventToSeq, sourceIDs).Scan(&count)
	if err != nil {
		return err
	}
	if count != len(sourceIDs) {
		return errors.New("domain: source_message_ids must reference visible customer messages from this conversation")
	}
	return nil
}

// PrepareModelTurn stores the immutable request and application dependency
// snapshot before the provider is called. Locking the run serializes activity
// retries so (run, seq) always resolves to one durable model turn.
func (s *Store) PrepareModelTurn(ctx context.Context, turn temporalkit.ModelTurnRecord) (*temporalkit.PreparedModelTurn, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var locked string
	if err := tx.QueryRow(ctx, `SELECT id FROM runs WHERE id=$1 AND org_id=$2 FOR UPDATE`, turn.RunID, turn.OrgID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if prepared, err := readPreparedModelTurn(ctx, tx, turn.RunID, turn.Seq); err == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return prepared, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	var conversationID string
	if err := tx.QueryRow(ctx, `SELECT rb.conversation_id
		FROM run_bindings rb JOIN conversations c ON c.id=rb.conversation_id AND c.org_id=rb.org_id
		WHERE rb.run_id=$1 AND rb.org_id=$2`, turn.RunID, turn.OrgID).Scan(&conversationID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	dependencies := turn.DependencyVersions
	if len(dependencies) == 0 {
		dependencies = json.RawMessage(`{}`)
	}
	triggeringMessageIDs := turn.TriggeringMessageIDs
	if triggeringMessageIDs == nil {
		triggeringMessageIDs = []string{}
	}
	contextRaw, err := json.Marshal(map[string]any{
		"agent": turn.Agent, "tags": turn.Tags, "request": turn.Request,
	})
	if err != nil {
		return nil, err
	}
	requestRaw, err := json.Marshal(turn.Request)
	if err != nil {
		return nil, err
	}
	snapshotID := agentkit.NewID()
	if _, err := tx.Exec(ctx, `INSERT INTO context_snapshots
		(id,org_id,conversation_id,run_id,context_revision,event_from_seq,event_to_seq,triggering_message_ids,dependency_versions,context)
		VALUES($1,$2,$3,$4,$5,0,$6,$7,$8,$9)`, snapshotID, turn.OrgID, conversationID,
		turn.RunID, turn.ContextRevision, turn.EventToSeq, triggeringMessageIDs, dependencies, contextRaw); err != nil {
		return nil, err
	}
	promptVersion := turn.Tags["prompt_version"]
	if promptVersion == "" {
		promptVersion = "unknown"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO model_turns
		(id,org_id,conversation_id,run_id,context_snapshot_id,prompt_version,request,usage,disposition,seq)
		VALUES($1,$2,$3,$4,$5,$6,$7,'{}','pending',$8)`, turn.CandidateID, turn.OrgID,
		conversationID, turn.RunID, snapshotID, promptVersion, requestRaw, turn.Seq); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &temporalkit.PreparedModelTurn{
		ID:                 turn.CandidateID,
		ContextRevision:    turn.ContextRevision,
		EventToSeq:         turn.EventToSeq,
		DependencyVersions: dependencies,
	}, nil
}

func readPreparedModelTurn(ctx context.Context, tx pgx.Tx, runID string, seq int) (*temporalkit.PreparedModelTurn, error) {
	var id string
	var raw []byte
	var contextRevision int64
	var eventToSeq int64
	var dependencies json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT mt.id,mt.response,cs.context_revision,cs.event_to_seq,cs.dependency_versions
		FROM model_turns mt JOIN context_snapshots cs ON cs.id=mt.context_snapshot_id
		WHERE mt.run_id=$1 AND mt.seq=$2`, runID, seq).
		Scan(&id, &raw, &contextRevision, &eventToSeq, &dependencies); err != nil {
		return nil, err
	}
	prepared := &temporalkit.PreparedModelTurn{
		ID:                 id,
		ContextRevision:    contextRevision,
		EventToSeq:         eventToSeq,
		DependencyVersions: dependencies,
	}
	if len(raw) > 0 {
		var response llm.CompletionResponse
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, err
		}
		prepared.Response = &response
	}
	return prepared, nil
}

// CompleteModelTurn records the first provider response. Later activity
// retries observe and reuse it; they never replace it with a divergent answer.
func (s *Store) CompleteModelTurn(ctx context.Context, id string, response *llm.CompletionResponse) (*llm.CompletionResponse, error) {
	raw, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	usage, err := json.Marshal(response.Usage)
	if err != nil {
		return nil, err
	}
	var canonical json.RawMessage
	err = s.pool.QueryRow(ctx, `UPDATE model_turns SET response=$2,usage=$3,disposition='completed'
		WHERE id=$1 AND response IS NULL RETURNING response`, id, raw, usage).Scan(&canonical)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.pool.QueryRow(ctx, `SELECT response FROM model_turns WHERE id=$1 AND response IS NOT NULL`, id).Scan(&canonical)
	}
	if err != nil {
		return nil, err
	}
	var stored llm.CompletionResponse
	if err := json.Unmarshal(canonical, &stored); err != nil {
		return nil, err
	}
	return &stored, nil
}
