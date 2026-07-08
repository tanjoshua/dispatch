// Package domain holds the dispatch product's data model: customers,
// conversations, messages, and jobs.
package domain

import (
	"encoding/json"
	"time"
)

// Organization is the tenant root every table keys on via org_id. Promoted in
// design/002 from a server-global constant to a real row; it will eventually
// own its channels, customers, playbook, and policy.
type Organization struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Settings  json.RawMessage `json:"settings,omitempty"` // open bag; typed fields graduate out as they earn it
	CreatedAt time.Time       `json:"created_at"`
}

// ChannelConnection is one org's configured use of a channel kind — this org's
// dev pane, or later its WhatsApp business number. It carries org identity into
// every inbound message (design/002): inbound resolves the org off the
// connection the message arrived on. Kind selects the code-defined adapter;
// the connection is the data.
type ChannelConnection struct {
	ID        string          `json:"id"`
	OrgID     string          `json:"org_id"`
	Kind      string          `json:"kind"`    // "dev" | "whatsapp" | ... — selects the adapter
	Address   string          `json:"address"` // business-side identity inbound is addressed to; inbound lookup key
	Config    json.RawMessage `json:"config,omitempty"`
	Status    string          `json:"status"` // "active" | "disabled"
	CreatedAt time.Time       `json:"created_at"`
}

type Customer struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Phone     string    `json:"phone"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type ConversationStatus string

const (
	ConversationOpen   ConversationStatus = "open"
	ConversationClosed ConversationStatus = "closed"
)

// AttentionState is the escalation projection: whether a human needs to
// engage with this conversation now. Orthogonal to Status — an open
// conversation can be flagged, and a flagged one is not auto-closed
// (design/001-escalation.md). "flagged" is the one that pulls the
// conversation to the top of the dispatcher's list.
type AttentionState string

const (
	AttentionNone         AttentionState = "none"         // never escalated
	AttentionFlagged      AttentionState = "flagged"      // agent raised; awaiting a human
	AttentionAcknowledged AttentionState = "acknowledged" // a dispatcher engaged
)

type Conversation struct {
	ID              string             `json:"id"`
	OrgID           string             `json:"org_id"`
	CustomerID      string             `json:"customer_id"`
	ChannelID       string             `json:"channel_id"` // the connection this conversation belongs to
	RunID           string             `json:"run_id,omitempty"` // current agent run
	Status          ConversationStatus `json:"status"`
	AttentionState  AttentionState     `json:"attention_state"`
	AttentionReason string             `json:"attention_reason,omitempty"`
	EscalatedAt     *time.Time         `json:"escalated_at,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

type Direction string

const (
	Inbound  Direction = "inbound"  // from the customer
	Outbound Direction = "outbound" // to the customer
)

type Message struct {
	ID             string    `json:"id"`
	OrgID          string    `json:"org_id"`
	ConversationID string    `json:"conversation_id"`
	Direction      Direction `json:"direction"`
	Body           string    `json:"body"`
	CreatedAt      time.Time `json:"created_at"`
}

type JobStatus string

const (
	JobIntake         JobStatus = "intake"
	JobIntakeComplete JobStatus = "intake_complete"
)

// Job is the structured record the intake agent builds up over the
// conversation. One job per conversation in v1.
type Job struct {
	ID             string    `json:"id"`
	OrgID          string    `json:"org_id"`
	ConversationID string    `json:"conversation_id"`
	CustomerName   string    `json:"customer_name"`
	Phone          string    `json:"phone"`
	Address        string    `json:"address"`
	Issue          string    `json:"issue"`
	Urgency        string    `json:"urgency"` // low | normal | high | emergency
	Status         JobStatus `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// JobPatch is a partial update; nil fields are left unchanged. Phone is
// deliberately absent: it's the customer's channel identity, seeded from the
// customer record when the job is created, never collected by the agent.
type JobPatch struct {
	CustomerName *string `json:"customer_name,omitempty"`
	Address      *string `json:"address,omitempty"`
	Issue        *string `json:"issue,omitempty"`
	Urgency      *string `json:"urgency,omitempty"`
}
