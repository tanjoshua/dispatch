// Package domain holds the dispatch product's data model: customers,
// conversations, messages, and cases.
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
	Version   int64           `json:"version"`
	CreatedAt time.Time       `json:"created_at"`
}

// ChannelConnection is one org's configured use of a channel kind — this org's
// dev pane, or later its WhatsApp business number. It carries org identity into
// every inbound message (design/002): inbound resolves the org off the
// connection the message arrived on. Kind selects the code-defined adapter;
// the connection is the data.
type ChannelConnection struct {
	ID                string          `json:"id"`
	OrgID             string          `json:"org_id"`
	Kind              string          `json:"kind"`    // "dev" | "whatsapp" | ... — selects the adapter
	Address           string          `json:"address"` // business-side identity inbound is addressed to; inbound lookup key
	Config            json.RawMessage `json:"config,omitempty"`
	Status            string          `json:"status"`              // "active" | "disabled"
	DefaultPlaybookID string          `json:"default_playbook_id"` // the playbook inbound on this connection routes to (design/004 §8)
	Version           int64           `json:"version"`
	CreatedAt         time.Time       `json:"created_at"`
}

// Playbook is the org-tailored config a channel connection routes inbound to: it
// selects the code agent (pack) that runs and names the case type it produces
// (design/004-domain-remodel.md §8). Variation across orgs/verticals happens by
// selecting and parameterizing code-defined capabilities here — never a no-code
// workflow engine. With one pack (field service) this is the selection seam; the
// pack SDK (config-parameterized prompts/schemas) is design/005.
type Playbook struct {
	ID        string          `json:"id"`
	OrgID     string          `json:"org_id"`
	Name      string          `json:"name"`
	Agent     string          `json:"agent"`     // names a code-registered agent definition
	CaseType  string          `json:"case_type"` // the case type this playbook produces
	Config    json.RawMessage `json:"config,omitempty"`
	Version   int64           `json:"version"`
	CreatedAt time.Time       `json:"created_at"`
}

// Customer is the CRM aggregate: a person or business the org serves. Contact
// endpoints (phone, email) live on ContactIdentity, not here — a customer is
// reachable at many identities across channels (design/004-domain-remodel.md §3).
type Customer struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

// ContactIdentity is one channel endpoint a customer is reachable at — a phone
// on WhatsApp/SMS, an email, a dev token. A customer has many; inbound resolves
// (ChannelKind, Address) -> identity -> customer, so the same person across
// channels is one customer (design/004-domain-remodel.md §3).
type ContactIdentity struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	CustomerID  string    `json:"customer_id"`
	ChannelKind string    `json:"channel_kind"`
	Address     string    `json:"address"`
	CreatedAt   time.Time `json:"created_at"`
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

// Conversation is the persistent messaging thread with a customer on a channel
// — one per (customer, channel), durable across many agent runs and cases
// (design/004-domain-remodel.md §4). It no longer carries "the" run; a run is a
// task bound to the thread in run_bindings, and a thread can have many over its
// life. Status stays for a future archive state; threads are never auto-closed.
type Conversation struct {
	ID                string             `json:"id"`
	OrgID             string             `json:"org_id"`
	CustomerID        string             `json:"customer_id"`
	ChannelID         string             `json:"channel_id"` // the connection this conversation belongs to
	ContactIdentityID string             `json:"contact_identity_id"`
	EventSeq          int64              `json:"event_seq"`
	ContextRevision   int64              `json:"context_revision"`
	Status            ConversationStatus `json:"status"`
	AttentionState    AttentionState     `json:"attention_state"`
	AttentionReason   string             `json:"attention_reason,omitempty"`
	EscalatedAt       *time.Time         `json:"escalated_at,omitempty"`
	// ThreadSummary is the rolling record of what past tasks on this thread
	// were about: one dated line per completed task, taken from the
	// dispatcher-approved close_case summary. Briefings feed it to fresh runs
	// so a returning customer isn't met cold (OVERVIEW §6.4).
	ThreadSummary string    `json:"thread_summary,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Direction string

const (
	Inbound  Direction = "inbound"  // from the customer
	Outbound Direction = "outbound" // to the customer
)

// MessageAuthor is who wrote a message. The dispatcher is a first-class
// participant (design/003-dispatcher-as-participant.md), so an outbound message
// is the agent's or the dispatcher's; inbound is always the customer's.
type MessageAuthor string

const (
	AuthorCustomer   MessageAuthor = "customer"   // inbound
	AuthorAgent      MessageAuthor = "agent"      // outbound via a send_message Action
	AuthorDispatcher MessageAuthor = "dispatcher" // outbound, sent directly by a human dispatcher
)

type Message struct {
	ID             string        `json:"id"`
	OrgID          string        `json:"org_id"`
	ConversationID string        `json:"conversation_id"`
	Direction      Direction     `json:"direction"`
	Author         MessageAuthor `json:"author"`
	Body           string        `json:"body"`
	// ProviderMessageID is the transport's own ID for an inbound message (a
	// WhatsApp wamid, etc.) — the dedupe key under webhook retries and provider
	// duplicates. Empty on outbound messages and on channels without one (dev).
	ProviderMessageID  string        `json:"provider_message_id,omitempty"`
	EventSeq           int64         `json:"event_seq,omitempty"`
	DeliveryState      DeliveryState `json:"delivery_state"`
	ProviderDeliveryID string        `json:"provider_delivery_id,omitempty"`
	DeliveryError      string        `json:"delivery_error,omitempty"`
	CreatedAt          time.Time     `json:"created_at"`
}

type DeliveryState string

const (
	DeliveryQueued  DeliveryState = "queued"
	DeliverySending DeliveryState = "sending"
	DeliverySent    DeliveryState = "sent"
	DeliveryFailed  DeliveryState = "failed"
	DeliveryUnknown DeliveryState = "unknown"
)

type CaseStatus string

const (
	CaseIntake         CaseStatus = "intake"
	CaseIntakeComplete CaseStatus = "intake_complete"
)

// Case is the unit of work the org fulfills for a customer — the generalization
// of a field-service "job" (design/004-domain-remodel.md §5). It has a typed
// core (customer, type, status, timestamps) plus a per-vertical Data bag whose
// schema the playbook owns; for the field-service pack, Data holds
// {address, issue, urgency}. Transitionally one per conversation; many-per-
// customer / many-per-thread arrives in Phase 3. The customer's name lives on
// the Customer and their contact on the ContactIdentity — neither is copied here.
type Case struct {
	ID             string          `json:"id"`
	OrgID          string          `json:"org_id"`
	CustomerID     string          `json:"customer_id"`
	ConversationID string          `json:"conversation_id"`
	Type           string          `json:"type"` // "field_service_job" — the playbook's case type
	Status         CaseStatus      `json:"status"`
	Version        int64           `json:"version"`
	Summary        string          `json:"summary"`
	Data           json.RawMessage `json:"data"` // per-vertical fields; schema owned by the playbook
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}
