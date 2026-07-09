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
	//
	// It is also the delivery idempotency key: callers that may retry a send
	// (the action pipeline derives it from the action ID) pass the same ID each
	// attempt, and adapters must dedupe on it — persistence via AddMessage
	// already does; a real transport passes it to its provider as the
	// idempotency key — so the customer receives the message at most once.
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
	to, err := s.store.ContactAddressForConversation(ctx, conversationID)
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
		To:             to,
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
	agent     string // fallback agent when a connection has no playbook; playbook selection is the norm (design/004 §8)
}

func NewRouter(store *domain.Store, ak akstore.Store, tc temporalclient.Client, taskQueue, agent string) *Router {
	return &Router{store: store, agentkit: ak, temporal: tc, taskQueue: taskQueue, agent: agent}
}

func (r *Router) Receive(ctx context.Context, conn domain.ChannelConnection, in InboundMessage) (RouteResult, error) {
	customer, err := r.store.GetOrCreateCustomerByIdentity(ctx, conn.OrgID, conn.Kind, in.From, in.Name)
	if err != nil {
		return RouteResult{}, err
	}

	// The persistent thread for this (customer, channel). It is durable across
	// runs and cases (design/004-domain-remodel.md §4) — get-or-create, never
	// gated on status.
	conv, err := r.store.ThreadForCustomerChannel(ctx, conn.OrgID, customer.ID, conn.ID)
	if errors.Is(err, domain.ErrNotFound) {
		conv, err = r.store.CreateConversation(ctx, conn.OrgID, customer.ID, conn.ID)
	}
	if err != nil {
		return RouteResult{}, err
	}

	// The connection routes inbound to a playbook, which selects the agent (pack)
	// that runs and the case type it produces (design/004 §8). Fall back to the
	// Router's default agent when a connection has no playbook.
	agentName := r.agent
	playbookID := ""
	if conn.DefaultPlaybookID != "" {
		pb, err := r.store.GetPlaybook(ctx, conn.DefaultPlaybookID)
		if err != nil {
			return RouteResult{}, fmt.Errorf("resolve playbook: %w", err)
		}
		agentName = pb.Agent
		playbookID = pb.ID
	}

	runID, err := r.ensureRun(ctx, conn.OrgID, conv, agentName, playbookID)
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
		temporalkit.AgentLoopInput{RunID: runID, OrgID: conn.OrgID, Agent: agentName},
	)
	if err != nil {
		return RouteResult{}, fmt.Errorf("signal workflow: %w", err)
	}

	return RouteResult{ConversationID: conv.ID, RunID: runID, MessageID: msg.ID}, nil
}

// ensureRun returns the thread's live run — the one awaiting customer input —
// creating a fresh task-run (and its binding) when the thread has none or its
// last run already finished. A finished run means the previous case is done; the
// new run will open a new case on the same persistent thread
// (design/004-domain-remodel.md §6).
func (r *Router) ensureRun(ctx context.Context, orgID string, conv *domain.Conversation, agentName, playbookID string) (string, error) {
	latest, err := r.store.LatestRunIDForConversation(ctx, conv.ID)
	if err != nil {
		return "", err
	}
	if latest != "" {
		run, err := r.agentkit.GetRun(ctx, latest)
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
		Agent:  agentName,
		Status: agentkit.RunRunning,
	}); err != nil {
		return "", err
	}
	if err := r.store.CreateRunBinding(ctx, runID, orgID, conv.ID, "intake", playbookID); err != nil {
		return "", err
	}
	return runID, nil
}
