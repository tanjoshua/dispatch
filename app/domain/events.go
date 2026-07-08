package domain

import "dispatch/agentkit"

// App-level event types on the run's append-only log. The agent's own
// actions (send_message, escalate, ...) already record themselves through
// agentkit's action pipeline; these name domain events that have no backing
// Action — a human act on the run.
const (
	// EventEscalationAcknowledged records a dispatcher engaging with a
	// flagged conversation. It is human-initiated (no agent Action proposed
	// it), so it lives on the log as its own event — the same way a
	// dispatcher's decision does. Payload: {conversation_id, acknowledged_by,
	// note}. See design/001-escalation.md §4.
	EventEscalationAcknowledged agentkit.EventType = "escalation_acknowledged"

	// EventDispatcherMessage records the dispatcher sending a message directly
	// to the customer (design/003-dispatcher-as-participant.md). Like an
	// acknowledgement it is a human act with no backing agent Action, so it is
	// its own event on the run's log. The outbound message row is the projection
	// the UI reads; this event is the audit entry. Payload: {conversation_id,
	// message_id, sent_by}.
	EventDispatcherMessage agentkit.EventType = "dispatcher_message"
)
