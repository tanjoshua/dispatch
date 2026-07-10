// Package store persists agentkit's runs, actions, and events.
//
// Mutating methods are idempotent under retries: proposals dedupe on
// (run_id, tool_call_id), decisions and results only apply once, and event
// appends dedupe on (run_id, dedupe_key). Activities can therefore be
// retried by Temporal without double-recording.
package store

import (
	"context"
	"encoding/json"

	"dispatch/agentkit"
	"dispatch/agentkit/llm"
)

// Store is the persistence interface the agent loop's activities (and any
// UI reading agentkit state) depend on.
//
// Reads that take an ID a client could supply also take the org ID and scope
// the query to it (OVERVIEW §6.2 #10): tenant isolation is enforced at the
// store signature, not left for each new endpoint to remember.
type Store interface {
	// CreateRun inserts the run; a run with the same ID is a no-op.
	CreateRun(ctx context.Context, run agentkit.Run) error
	GetRun(ctx context.Context, orgID, id string) (*agentkit.Run, error)
	// FinishRun sets a terminal status and appends the given event.
	FinishRun(ctx context.Context, runID string, status agentkit.RunStatus, event agentkit.Event) error

	// ProposeAction inserts the action and its action_proposed event. If an
	// action with the same (run_id, tool_call_id) exists, the stored action
	// is returned instead — making proposal retries safe.
	ProposeAction(ctx context.Context, action agentkit.Action, event agentkit.Event) (*agentkit.Action, error)
	// RecordDecision applies a decision to an undecided action and appends
	// the decision_made event. If the action is already decided, the stored
	// action is returned unchanged.
	RecordDecision(ctx context.Context, actionID string, decision agentkit.Decision, editedInput json.RawMessage, event agentkit.Event) (*agentkit.Action, error)
	// FinishAction records an execution result (or failure) and appends the
	// corresponding event. Already-finished actions are returned unchanged.
	FinishAction(ctx context.Context, actionID string, result json.RawMessage, execErr string, event agentkit.Event) (*agentkit.Action, error)

	GetAction(ctx context.Context, orgID, id string) (*agentkit.Action, error)
	ListActionsByRun(ctx context.Context, orgID, runID string) ([]agentkit.Action, error)
	// DecisionStats aggregates decision outcomes and human-decision latency
	// per tool for one org — the review-queue evidence (pending age,
	// approval/edit/rejection rates) the autonomy policy is tuned on.
	DecisionStats(ctx context.Context, orgID string) ([]agentkit.ToolDecisionStats, error)

	// AppendEvent appends one event, ignoring duplicates by dedupe key.
	AppendEvent(ctx context.Context, event agentkit.Event) error
	ListEventsByRun(ctx context.Context, orgID, runID string) ([]agentkit.Event, error)

	// The run transcript: the agent's conversation context, one row per
	// message, sequence numbers assigned by the workflow (deterministic under
	// replay). Appends are idempotent per (run_id, seq) — the first write
	// wins — so retried activities never duplicate or rewrite a turn.
	AppendRunMessages(ctx context.Context, runID, orgID string, baseSeq int, msgs []llm.Message) error
	// ListRunMessages returns transcript rows with seq < upTo, in order. upTo
	// bounds the read to what the caller knows exists, so a retried activity
	// assembles the same context it did the first time.
	ListRunMessages(ctx context.Context, runID string, upTo int) ([]llm.Message, error)
	// GetRunMessage returns the message at seq, or ok=false if absent.
	GetRunMessage(ctx context.Context, runID string, seq int) (msg *llm.Message, ok bool, err error)
}
