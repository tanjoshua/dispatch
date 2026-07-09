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
	_, conv, err := runAndThread(ctx, store)
	return conv, err
}

// runAndThread resolves both the run id and its thread from the RunContext. The
// case a run works is bound to the run (not the thread), so the case tools need
// the run id (design/004-domain-remodel.md §6).
func runAndThread(ctx context.Context, store *domain.Store) (string, *domain.Conversation, error) {
	rc, ok := agentkit.RunContextFrom(ctx)
	if !ok {
		return "", nil, fmt.Errorf("intake: no run context (tool executed outside the action pipeline?)")
	}
	conv, err := store.GetConversationByRunID(ctx, rc.RunID)
	return rc.RunID, conv, err
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
	rc, ok := agentkit.RunContextFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("send_message: no run context (tool executed outside the action pipeline?)")
	}
	conv, err := t.store.GetConversationByRunID(ctx, rc.RunID)
	if err != nil {
		return nil, err
	}
	// The message ID is the action ID: a retried execution (crash between the
	// send and FinishAction) re-delivers under the same ID, which persistence
	// and real providers dedupe on — the customer never gets it twice.
	if err := t.sender.Send(ctx, conv.ID, channel.OutboundMessage{
		Body:   in.Message,
		Author: domain.AuthorAgent,
		ID:     rc.ActionID,
	}); err != nil {
		return nil, fmt.Errorf("send_message: %w", err)
	}
	return json.RawMessage(`{"status":"sent"}`), nil
}

// --- update_case ---

// updateCaseTool records what the agent learns into the structured case record.
// customer_name is an attribute of the customer (the CRM aggregate), so it is
// routed there; the remaining fields are the field-service pack's case-schema
// fields and are merged into the case's Data bag. The input schema is hardcoded
// to the field-service fields for now; it becomes playbook-derived in Phase 4
// (design/004 §5, §8).
type updateCaseTool struct {
	store *domain.Store
}

func (t *updateCaseTool) Name() string { return "update_case" }

func (t *updateCaseTool) Description() string {
	return "Create or update the structured job record for this conversation. Pass only the fields you have new information for; existing values are kept. The customer's phone number is already known from the channel — don't ask for it."
}

func (t *updateCaseTool) InputSchema() json.RawMessage {
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

func (t *updateCaseTool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return nil, fmt.Errorf("update_case: invalid input: %w", err)
	}
	runID, conv, err := runAndThread(ctx, t.store)
	if err != nil {
		return nil, err
	}
	// customer_name belongs on the customer, not the case — pull it out and
	// route it there; the rest is the case's per-vertical data.
	if raw, ok := fields["customer_name"]; ok {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return nil, fmt.Errorf("update_case: invalid customer_name: %w", err)
		}
		if err := t.store.SetCustomerName(ctx, conv.CustomerID, name); err != nil {
			return nil, fmt.Errorf("update_case: %w", err)
		}
		delete(fields, "customer_name")
	}
	patch, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("update_case: %w", err)
	}
	c, err := t.store.UpsertCaseForRun(ctx, runID, conv.OrgID, conv.ID, patch)
	if err != nil {
		return nil, fmt.Errorf("update_case: %w", err)
	}
	return json.Marshal(c)
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

// --- continue_case ---

// continueCaseTool attaches a triage run to the thread's most recent case, so
// the customer's follow-up updates the job they already reported instead of
// opening a duplicate (OVERVIEW §6.3 #11). Auto-approved: like update_case it
// is internal record-keeping — anything customer-visible still goes through
// send_message and its policy.
type continueCaseTool struct {
	store *domain.Store
}

type continueCaseInput struct {
	Reason string `json:"reason"`
}

func (t *continueCaseTool) Name() string { return "continue_case" }

func (t *continueCaseTool) Description() string {
	return "Attach this task to the customer's most recent existing job on this thread instead of opening a new one — use when their message adds to, corrects, or reopens that job. After this, update_case edits that job. Needs no approval."
}

func (t *continueCaseTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"reason": {"type": "string", "description": "One line: why this message belongs to the existing job."}
		},
		"required": ["reason"]
	}`)
}

func (t *continueCaseTool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var in continueCaseInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("continue_case: invalid input: %w", err)
	}
	if in.Reason == "" {
		return nil, fmt.Errorf("continue_case: reason is empty")
	}
	runID, _, err := runAndThread(ctx, t.store)
	if err != nil {
		return nil, err
	}
	c, err := t.store.BindRunToLatestCase(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("continue_case: %w", err)
	}
	return json.Marshal(c)
}

// --- close_case ---

type closeCaseTool struct {
	store *domain.Store
}

type closeCaseInput struct {
	Summary string `json:"summary"`
}

func (t *closeCaseTool) Name() string { return "close_case" }

func (t *closeCaseTool) Description() string {
	return "Mark this task complete. For an intake: only call after the job record has the customer's name, address, issue, and urgency, and you have sent the customer a recap. If this task didn't touch a job record (you were only answering a question), this simply ends the task."
}

func (t *closeCaseTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"summary": {"type": "string", "description": "One-line summary of the job for the dispatcher."}
		},
		"required": ["summary"]
	}`)
}

func (t *closeCaseTool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var in closeCaseInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("close_case: invalid input: %w", err)
	}
	runID, _, err := runAndThread(ctx, t.store)
	if err != nil {
		return nil, err
	}
	c, err := t.store.CompleteCaseForRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("close_case: %w", err)
	}
	if c == nil {
		// A triage run that never touched a case (answered a question) ends
		// with no case — the run completes all the same.
		return json.Marshal(map[string]any{"status": "task_complete", "summary": in.Summary})
	}
	return json.Marshal(map[string]any{"status": "intake_complete", "case": c, "summary": in.Summary})
}
