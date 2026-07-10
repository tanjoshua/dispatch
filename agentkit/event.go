package agentkit

import (
	"encoding/json"
	"time"
)

// EventType names what happened in a run.
type EventType string

const (
	EventMessageReceived EventType = "message_received"
	EventActionProposed  EventType = "action_proposed"
	EventDecisionMade    EventType = "decision_made"
	EventActionExecuted  EventType = "action_executed"
	EventActionFailed    EventType = "action_failed"
	EventRunCompleted    EventType = "run_completed"
	EventRunFailed       EventType = "run_failed"
	// EventLLMCompleted records one LLM completion's usage (model, tokens,
	// stop reason) — the cost/billing/eval substrate. Appended by the Complete
	// activity, keyed on the run's completion sequence number.
	EventLLMCompleted EventType = "llm_completed"
	// EventTurnBudgetExceeded records that an agent turn hit its LLM-call
	// budget and was stopped; the application reacts (e.g. summons a human)
	// via the hook on temporalkit.Activities.
	EventTurnBudgetExceeded EventType = "turn_budget_exceeded"
	// EventDecisionDropped records a human decision that arrived for an
	// action that was no longer (or never) pending — a supersede race, a
	// second dispatcher's ruling after the first landed, a stale retry. The
	// decision API acks before the workflow consumes the signal, so without
	// this event the ruling would vanish from the audit trail (OVERVIEW
	// §6.1 #4).
	EventDecisionDropped EventType = "decision_dropped"
)

// Event is one entry in a run's append-only log — the audit trail, the
// source for UI projections, and raw material for future analytics. Events
// are never updated or deleted.
//
// DedupeKey makes appends idempotent under activity retries: writers derive
// it from the business key (e.g. "action_proposed:{actionID}") and the store
// ignores appends whose (run_id, dedupe_key) already exists.
type Event struct {
	ID        string          `json:"id"`
	OrgID     string          `json:"org_id"`
	RunID     string          `json:"run_id"`
	Type      EventType       `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	DedupeKey string          `json:"dedupe_key"`
	CreatedAt time.Time       `json:"created_at"`
}
