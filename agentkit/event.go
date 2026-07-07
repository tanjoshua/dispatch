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
