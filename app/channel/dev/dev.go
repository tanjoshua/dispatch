// Package dev is the local-development channel: a pane in the web UI where a
// developer types as the customer. It is a genuine channel connection, not a
// bypass — inbound goes through the shared Router and outbound through the
// shared Sender, so the dev channel exercises the production path. The only
// kind-specific behavior is Deliver writing the outbound Message row the pane
// renders; a real transport would call its provider API instead
// (design/002-organization-and-channels.md §6).
package dev

import (
	"context"

	"dispatch/agentkit"
	"dispatch/app/channel"
	"dispatch/app/domain"
)

// Name is the channel kind; Address is the seeded dev connection's address, the
// key the dev inbound endpoint resolves its connection by.
const (
	Name    = "dev"
	Address = "dev"
)

type Adapter struct {
	store *domain.Store
}

func New(store *domain.Store) *Adapter { return &Adapter{store: store} }

func (a *Adapter) Kind() string { return Name }

// Deliver writes the outbound message to the conversation; the UI pane renders
// it. This is the only kind-specific behavior — everything upstream is shared.
func (a *Adapter) Deliver(ctx context.Context, d channel.Delivery) error {
	return a.store.AddMessage(ctx, domain.Message{
		ID:             agentkit.NewID(),
		OrgID:          d.Conn.OrgID,
		ConversationID: d.ConversationID,
		Direction:      domain.Outbound,
		Body:           d.Msg.Body,
	})
}
