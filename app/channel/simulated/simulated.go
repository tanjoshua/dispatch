// Package simulated is the v1 channel: a pane in the web UI where you type
// as the customer. Outbound messages are written to the conversation and
// rendered in the pane; inbound goes through the same signal path a real
// webhook would (see the server's simulate endpoint).
package simulated

import (
	"context"

	"dispatch/agentkit"
	"dispatch/app/channel"
	"dispatch/app/domain"
)

const Name = "simulated"

type Channel struct {
	store *domain.Store
}

func New(store *domain.Store) *Channel { return &Channel{store: store} }

func (c *Channel) Name() string { return Name }

func (c *Channel) Send(ctx context.Context, conversationID string, msg channel.OutboundMessage) error {
	conv, err := c.store.GetConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	return c.store.AddMessage(ctx, domain.Message{
		ID:             agentkit.NewID(),
		OrgID:          conv.OrgID,
		ConversationID: conversationID,
		Direction:      domain.Outbound,
		Body:           msg.Body,
	})
}
