// Package channel abstracts the bidirectional message transport with a
// customer. The agent, workflow, and UI never know which channel is in use.
//
// A channel *kind* is code (an Adapter: how to send/receive on a transport); a
// channel *connection* is data (one org's configured use of a kind — see
// domain.ChannelConnection). Two shared services own the paths that matter:
//
//   - Sender  — shared outbound path. Resolves a conversation's connection,
//     picks the adapter by kind, delivers. The send_message tool holds this.
//   - Router  — shared inbound path. Every transport edge (dev endpoint, a
//     future WhatsApp webhook) resolves its connection and calls Receive, which
//     resolves org from the connection and drives the run.
//
// Per-kind Adapters are thin transport edges; Sender and Router are the
// production path, so the dev channel exercises the same code as prod
// (design/002-organization-and-channels.md).
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	temporalclient "go.temporal.io/sdk/client"

	"dispatch/agentkit"
	akstore "dispatch/agentkit/store"
	"dispatch/agentkit/temporalkit"
	"dispatch/app/domain"
)

type OutboundMessage struct {
	Body string `json:"body"`
	// Author is who is sending — the agent (a send_message Action) or a
	// dispatcher replying directly (design/003). Adapters persist it so the UI
	// and the agent context can tell them apart; it does not affect transport.
	Author domain.MessageAuthor `json:"author"`
	// ID optionally pins the persisted message's ID so the caller can reference
	// it (e.g. the dispatcher-reply endpoint needs it for the event + signal).
	// Empty means the adapter assigns one.
	ID string `json:"id,omitempty"`
}

// InboundMessage is a normalized message arriving on a connection: who it is
// from (the customer's channel address), an optional display name, and the text.
type InboundMessage struct {
	From string `json:"from"`
	Name string `json:"name"`
	Text string `json:"text"`
}

// RouteResult reports what an inbound message resolved to.
type RouteResult struct {
	ConversationID string `json:"conversation_id"`
	RunID          string `json:"run_id"`
	MessageID      string `json:"message_id"`
}

// Delivery is one outbound message resolved to a concrete destination. Conn and
// To let a real adapter address the transport; ConversationID lets the dev
// adapter write the outbound row the UI renders.
type Delivery struct {
	Conn           domain.ChannelConnection
	ConversationID string
	To             string // the customer's channel address (phone, etc.)
	Msg            OutboundMessage
}

// Adapter is one channel kind, registered at startup. Thin: transport only.
type Adapter interface {
	Kind() string
	Deliver(ctx context.Context, d Delivery) error
}

// Registry maps channel kinds to their adapters.
type Registry map[string]Adapter

func NewRegistry(adapters ...Adapter) Registry {
	r := make(Registry, len(adapters))
	for _, a := range adapters {
		r[a.Kind()] = a
	}
	return r
}

// Sender is the shared outbound path. It resolves the conversation's
// connection, picks the adapter by kind, and delivers.
type Sender struct {
	store *domain.Store
	reg   Registry
}

func NewSender(store *domain.Store, reg Registry) *Sender {
	return &Sender{store: store, reg: reg}
}

func (s *Sender) Send(ctx context.Context, conversationID string, msg OutboundMessage) error {
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	conn, err := s.store.GetChannelConnection(ctx, conv.ChannelID)
	if err != nil {
		return err
	}
	cust, err := s.store.GetCustomer(ctx, conv.CustomerID)
	if err != nil {
		return err
	}
	adapter, ok := s.reg[conn.Kind]
	if !ok {
		return fmt.Errorf("channel: no adapter registered for kind %q", conn.Kind)
	}
	return adapter.Deliver(ctx, Delivery{
		Conn:           *conn,
		ConversationID: conversationID,
		To:             cust.Phone,
		Msg:            msg,
	})
}

// Router is the shared inbound path. Every transport edge resolves the
// ChannelConnection its message arrived on and calls Receive; Receive resolves
// org *from the connection* (not a process global), finds/creates the
// conversation and run, persists the message + event, and signal-with-starts
// the run's workflow.
type Router struct {
	store     *domain.Store
	agentkit  akstore.Store
	temporal  temporalclient.Client
	taskQueue string
	agent     string // the single seeded playbook until playbook selection lands (design/002 §10)
}

func NewRouter(store *domain.Store, ak akstore.Store, tc temporalclient.Client, taskQueue, agent string) *Router {
	return &Router{store: store, agentkit: ak, temporal: tc, taskQueue: taskQueue, agent: agent}
}

func (r *Router) Receive(ctx context.Context, conn domain.ChannelConnection, in InboundMessage) (RouteResult, error) {
	customer, err := r.store.GetOrCreateCustomer(ctx, conn.OrgID, in.From, in.Name)
	if err != nil {
		return RouteResult{}, err
	}

	conv, err := r.store.OpenConversationForCustomer(ctx, conn.OrgID, customer.ID)
	if errors.Is(err, domain.ErrNotFound) {
		conv, err = r.store.CreateConversation(ctx, conn.OrgID, customer.ID, conn.ID)
	}
	if err != nil {
		return RouteResult{}, err
	}

	runID, err := r.ensureRun(ctx, conn.OrgID, conv)
	if err != nil {
		return RouteResult{}, err
	}

	msg := domain.Message{
		ID:             agentkit.NewID(),
		OrgID:          conn.OrgID,
		ConversationID: conv.ID,
		Direction:      domain.Inbound,
		Author:         domain.AuthorCustomer,
		Body:           in.Text,
	}
	if err := r.store.AddMessage(ctx, msg); err != nil {
		return RouteResult{}, err
	}
	payload, _ := json.Marshal(map[string]string{"message_id": msg.ID, "conversation_id": conv.ID})
	if err := r.agentkit.AppendEvent(ctx, agentkit.Event{
		ID:        agentkit.NewID(),
		OrgID:     conn.OrgID,
		RunID:     runID,
		Type:      agentkit.EventMessageReceived,
		Payload:   payload,
		DedupeKey: "message_received:" + msg.ID,
	}); err != nil {
		return RouteResult{}, err
	}

	_, err = r.temporal.SignalWithStartWorkflow(ctx,
		agentkit.WorkflowID(runID),
		temporalkit.SignalInboundMessage,
		temporalkit.InboundMessage{MessageID: msg.ID, Text: in.Text},
		temporalclient.StartWorkflowOptions{
			ID:        agentkit.WorkflowID(runID),
			TaskQueue: r.taskQueue,
		},
		temporalkit.AgentLoopWorkflowName,
		temporalkit.AgentLoopInput{RunID: runID, OrgID: conn.OrgID, Agent: r.agent},
	)
	if err != nil {
		return RouteResult{}, fmt.Errorf("signal workflow: %w", err)
	}

	return RouteResult{ConversationID: conv.ID, RunID: runID, MessageID: msg.ID}, nil
}

// ensureRun returns the conversation's live run, creating a fresh one when the
// conversation has none or its last run already finished.
func (r *Router) ensureRun(ctx context.Context, orgID string, conv *domain.Conversation) (string, error) {
	if conv.RunID != "" {
		run, err := r.agentkit.GetRun(ctx, conv.RunID)
		if err != nil && !errors.Is(err, akstore.ErrNotFound) {
			return "", err
		}
		if err == nil && run.Status == agentkit.RunRunning {
			return run.ID, nil
		}
	}
	runID := agentkit.NewID()
	if err := r.agentkit.CreateRun(ctx, agentkit.Run{
		ID:     runID,
		OrgID:  orgID,
		Agent:  r.agent,
		Status: agentkit.RunRunning,
	}); err != nil {
		return "", err
	}
	if err := r.store.SetConversationRun(ctx, conv.ID, runID); err != nil {
		return "", err
	}
	return runID, nil
}
