// Package channel abstracts the bidirectional message transport with a
// customer. The agent, workflow, and UI never know which channel is in use.
//
// Outbound goes through Channel.Send. Inbound is handled per adapter: each
// one (webhook handler, simulator endpoint) normalizes the incoming message,
// persists it, and signals the conversation's workflow.
package channel

import "context"

type OutboundMessage struct {
	Body string `json:"body"`
}

type Channel interface {
	Name() string // "simulated", "whatsapp", "sms"
	Send(ctx context.Context, conversationID string, msg OutboundMessage) error
}
