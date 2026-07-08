package intake

import (
	"context"
	"encoding/json"
	"fmt"

	"dispatch/agentkit"
	"dispatch/app/channel"
	"dispatch/app/domain"
)

// conversationFor resolves the conversation a tool call belongs to via the
// RunContext injected by the action pipeline.
func conversationFor(ctx context.Context, store *domain.Store) (*domain.Conversation, error) {
	rc, ok := agentkit.RunContextFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("intake: no run context (tool executed outside the action pipeline?)")
	}
	return store.GetConversationByRunID(ctx, rc.RunID)
}

// --- send_message ---

type sendMessageTool struct {
	store  *domain.Store
	sender *channel.Sender
}

type sendMessageInput struct {
	Message string `json:"message"`
}

func (t *sendMessageTool) Name() string { return "send_message" }

func (t *sendMessageTool) Description() string {
	return "Send a WhatsApp reply to the customer. This is the only way to talk to them."
}

func (t *sendMessageTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"message": {"type": "string", "description": "The message text to send to the customer."}
		},
		"required": ["message"]
	}`)
}

func (t *sendMessageTool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var in sendMessageInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("send_message: invalid input: %w", err)
	}
	if in.Message == "" {
		return nil, fmt.Errorf("send_message: message is empty")
	}
	conv, err := conversationFor(ctx, t.store)
	if err != nil {
		return nil, err
	}
	if err := t.sender.Send(ctx, conv.ID, channel.OutboundMessage{Body: in.Message, Author: domain.AuthorAgent}); err != nil {
		return nil, fmt.Errorf("send_message: %w", err)
	}
	return json.RawMessage(`{"status":"sent"}`), nil
}

// --- update_job ---

type updateJobTool struct {
	store *domain.Store
}

func (t *updateJobTool) Name() string { return "update_job" }

func (t *updateJobTool) Description() string {
	return "Create or update the structured job record for this conversation. Pass only the fields you have new information for; existing values are kept. The customer's phone number is already known from the channel — don't ask for it."
}

func (t *updateJobTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"customer_name": {"type": "string", "description": "The customer's name."},
			"address": {"type": "string", "description": "The service address."},
			"issue": {"type": "string", "description": "Clear description of the problem."},
			"urgency": {"type": "string", "enum": ["low", "normal", "high", "emergency"], "description": "How urgent the job is."}
		},
		"required": []
	}`)
}

func (t *updateJobTool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var patch domain.JobPatch
	if err := json.Unmarshal(input, &patch); err != nil {
		return nil, fmt.Errorf("update_job: invalid input: %w", err)
	}
	conv, err := conversationFor(ctx, t.store)
	if err != nil {
		return nil, err
	}
	job, err := t.store.UpsertJob(ctx, conv.OrgID, conv.ID, patch)
	if err != nil {
		return nil, fmt.Errorf("update_job: %w", err)
	}
	return json.Marshal(job)
}

// --- escalate ---

// escalateTool lets the agent summon a human to the run *now* — distinct from
// proposing a customer-facing action. It is orthogonal to approval: it does
// not decide whether an action executes (the policy still does), only how
// urgently a dispatcher engages. See design/001-escalation.md.
type escalateTool struct {
	store *domain.Store
}

type escalateInput struct {
	Reason   string `json:"reason"`
	Category string `json:"category"`
}

func (t *escalateTool) Name() string { return "escalate" }

func (t *escalateTool) Description() string {
	return "Flag this conversation for urgent human attention now — call it whenever you judge that a situation is unsafe or that a human dispatcher should step in. This pages the dispatcher and pulls the conversation to the top of their queue. It doesn't send the customer anything, so if there's a safety step they should take, send that too. Raising it needs no approval."
}

func (t *escalateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"reason": {"type": "string", "description": "One line for the dispatcher: what's happening and what you've told the customer."},
			"category": {"type": "string", "enum": ["emergency", "stuck", "other"], "description": "Why you're escalating. Use emergency for danger to people or property."}
		},
		"required": ["reason", "category"]
	}`)
}

func (t *escalateTool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var in escalateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("escalate: invalid input: %w", err)
	}
	if in.Reason == "" {
		return nil, fmt.Errorf("escalate: reason is empty")
	}
	conv, err := conversationFor(ctx, t.store)
	if err != nil {
		return nil, err
	}
	if err := t.store.RaiseEscalation(ctx, conv.ID, in.Reason); err != nil {
		return nil, fmt.Errorf("escalate: %w", err)
	}
	return json.RawMessage(`{"status":"escalated"}`), nil
}

// --- close_job ---

type closeJobTool struct {
	store *domain.Store
}

type closeJobInput struct {
	Summary string `json:"summary"`
}

func (t *closeJobTool) Name() string { return "close_job" }

func (t *closeJobTool) Description() string {
	return "Mark intake complete and end the conversation. Only call this after the job record has the customer's name, address, issue, and urgency, and you have sent the customer a recap."
}

func (t *closeJobTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"summary": {"type": "string", "description": "One-line summary of the job for the dispatcher."}
		},
		"required": ["summary"]
	}`)
}

func (t *closeJobTool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var in closeJobInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("close_job: invalid input: %w", err)
	}
	conv, err := conversationFor(ctx, t.store)
	if err != nil {
		return nil, err
	}
	job, err := t.store.CompleteIntake(ctx, conv.ID)
	if err != nil {
		return nil, fmt.Errorf("close_job: %w", err)
	}
	return json.Marshal(map[string]any{"status": "intake_complete", "job": job, "summary": in.Summary})
}
