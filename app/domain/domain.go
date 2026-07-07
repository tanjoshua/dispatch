// Package domain holds the dispatch product's data model: customers,
// conversations, messages, and jobs.
package domain

import "time"

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

type Conversation struct {
	ID         string             `json:"id"`
	OrgID      string             `json:"org_id"`
	CustomerID string             `json:"customer_id"`
	Channel    string             `json:"channel"`
	RunID      string             `json:"run_id,omitempty"` // current agent run
	Status     ConversationStatus `json:"status"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
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

// JobPatch is a partial update; nil fields are left unchanged.
type JobPatch struct {
	CustomerName *string `json:"customer_name,omitempty"`
	Phone        *string `json:"phone,omitempty"`
	Address      *string `json:"address,omitempty"`
	Issue        *string `json:"issue,omitempty"`
	Urgency      *string `json:"urgency,omitempty"`
}
