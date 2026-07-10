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
)

// Store is the persistence interface the agent loop's activities (and any
// UI reading agentkit state) depend on.
type Store interface {
	// CreateRun inserts the run; a run with the same ID is a no-op.
	CreateRun(ctx context.Context, run agentkit.Run) error
	GetRun(ctx context.Context, id string) (*agentkit.Run, error)
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

	GetAction(ctx context.Context, id string) (*agentkit.Action, error)
	ListActionsByRun(ctx context.Context, runID string) ([]agentkit.Action, error)
	// DecisionStats aggregates decision outcomes and human-decision latency
	// per tool for one org — the review-queue evidence (pending age,
	// approval/edit/rejection rates) the autonomy policy is tuned on.
	DecisionStats(ctx context.Context, orgID string) ([]agentkit.ToolDecisionStats, error)

	// AppendEvent appends one event, ignoring duplicates by dedupe key.
	AppendEvent(ctx context.Context, event agentkit.Event) error
	ListEventsByRun(ctx context.Context, runID string) ([]agentkit.Event, error)
}
