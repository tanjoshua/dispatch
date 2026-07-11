package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"dispatch/agentkit"
	"dispatch/app/channel"
	"dispatch/app/domain"
	"dispatch/app/notify"
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

// proposeResponseTool is the reviewed customer-facing unit. Approval releases
// one response; intake completion is applied only after the adapter confirms
// delivery returned successfully.
type proposeResponseTool struct {
	store  *domain.Store
	sender *channel.Sender
}
type proposeResponseInput struct {
	Message                 string `json:"message"`
	RespondsThroughEventSeq int64  `json:"responds_through_event_seq"`
	AfterDelivery           struct {
		CompleteRun        bool   `json:"complete_run"`
		MarkIntakeComplete bool   `json:"mark_intake_complete"`
		Summary            string `json:"summary"`
	} `json:"after_delivery"`
}

func (t *proposeResponseTool) Name() string { return "propose_response" }
func (t *proposeResponseTool) Description() string {
	return "Propose the single reviewed customer reply and optional delivery-dependent intake completion."
}
func (t *proposeResponseTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"message":{"type":"string","minLength":1},"responds_through_event_seq":{"type":"integer","minimum":1},"after_delivery":{"type":"object","properties":{"complete_run":{"type":"boolean"},"mark_intake_complete":{"type":"boolean"},"summary":{"type":"string"}},"required":["complete_run","mark_intake_complete","summary"],"additionalProperties":false}},"required":["message","responds_through_event_seq","after_delivery"],"additionalProperties":false}`)
}
func (t *proposeResponseTool) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in proposeResponseInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("propose_response: %w", err)
	}
	rc, ok := agentkit.RunContextFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("propose_response: no run context")
	}
	conv, err := t.store.GetConversationByRunID(ctx, rc.RunID)
	if err != nil {
		return nil, err
	}
	if conv.EventSeq != in.RespondsThroughEventSeq {
		return nil, fmt.Errorf("propose_response: stale context: conversation is at event %d", conv.EventSeq)
	}
	if err := t.sender.Send(ctx, conv.OrgID, conv.ID, channel.OutboundMessage{Body: in.Message, Author: domain.AuthorAgent, ID: rc.ActionID}); err != nil {
		return nil, err
	}
	if err := t.store.TransitionRunToInquiry(ctx, rc.RunID); err != nil { return nil, err }
	if in.AfterDelivery.MarkIntakeComplete {
		if _, err := t.store.CompleteCaseForRun(ctx, rc.RunID); err != nil {
			return nil, err
		}
	}
	if in.AfterDelivery.Summary != "" {
		line := time.Now().Format("2006-01-02") + " — " + in.AfterDelivery.Summary
		if err := t.store.AppendThreadSummary(ctx, conv.ID, line); err != nil {
			return nil, err
		}
	}
	return json.Marshal(map[string]any{"status": "sent", "complete_run": in.AfterDelivery.CompleteRun})
}

type pauseTool struct{ name string }

func (t *pauseTool) Name() string { return t.name }
func (t *pauseTool) Description() string {
	if t.name == "stand_down" {
		return "Stand down without messaging the customer."
	}
	return "Pause until external information or a new event arrives."
}
func (t *pauseTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"],"additionalProperties":false}`)
}
func (t *pauseTool) Execute(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	return raw, nil
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

type updateCaseInput struct {
	CaseID           string          `json:"case_id"`
	ExpectedVersion  int64           `json:"expected_version"`
	Patch            json.RawMessage `json:"patch"`
	SourceMessageIDs []string        `json:"source_message_ids"`
}

func (t *updateCaseTool) Name() string { return "update_case" }

func (t *updateCaseTool) Description() string {
	return "Update one explicitly selected customer-owned case using compare-and-set. Exact source message IDs are required."
}

func (t *updateCaseTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"case_id": {"type":"string"},
			"expected_version": {"type":"integer","minimum":1},
			"patch": {"type":"object","properties":{"address":{"type":"string"},"issue":{"type":"string"},"urgency":{"type":"string","enum":["low","normal","high","emergency"]}},"additionalProperties":false},
			"source_message_ids": {"type":"array","items":{"type":"string"},"minItems":1}
		},
		"required": ["case_id","expected_version","patch","source_message_ids"],
		"additionalProperties": false
	}`)
}

func (t *updateCaseTool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var in updateCaseInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("update_case: invalid input: %w", err)
	}
	runID, _, err := runAndThread(ctx, t.store)
	if err != nil {
		return nil, err
	}
	if in.CaseID == "" || in.ExpectedVersion < 1 || len(in.SourceMessageIDs) == 0 {
		return nil, fmt.Errorf("update_case: explicit case_id, expected_version, and source_message_ids are required")
	}
	c, err := t.store.UpdateCase(ctx, runID, in.CaseID, in.ExpectedVersion, in.Patch, in.SourceMessageIDs)
	if err != nil {
		return nil, fmt.Errorf("update_case: %w", err)
	}
	return json.Marshal(c)
}

// --- explicit case resolution ---
type listCandidateCasesTool struct{ store *domain.Store }

func (t *listCandidateCasesTool) Name() string { return "list_candidate_cases" }
func (t *listCandidateCasesTool) Description() string {
	return "List all ongoing and up to three recent completed cases for the resolved customer. Read only."
}
func (t *listCandidateCasesTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t *listCandidateCasesTool) Execute(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	_, conv, err := runAndThread(ctx, t.store)
	if err != nil {
		return nil, err
	}
	cs, err := t.store.ListCasesForCustomer(ctx, conv.OrgID, conv.CustomerID, 3)
	if err != nil {
		return nil, err
	}
	return json.Marshal(cs)
}

type selectCaseTool struct{ store *domain.Store }
type selectCaseInput struct {
	CaseID           string   `json:"case_id"`
	Reason           string   `json:"reason"`
	SourceMessageIDs []string `json:"source_message_ids"`
}

func (t *selectCaseTool) Name() string { return "select_case" }
func (t *selectCaseTool) Description() string {
	return "Explicitly bind this run to an existing customer-owned case; never use recency as the reason."
}
func (t *selectCaseTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"case_id":{"type":"string"},"reason":{"type":"string"},"source_message_ids":{"type":"array","items":{"type":"string"},"minItems":1}},"required":["case_id","reason","source_message_ids"],"additionalProperties":false}`)
}
func (t *selectCaseTool) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in selectCaseInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	runID, _, err := runAndThread(ctx, t.store)
	if err != nil {
		return nil, err
	}
	c, err := t.store.SelectCaseForRun(ctx, runID, in.CaseID, in.Reason, in.SourceMessageIDs)
	if err != nil {
		return nil, err
	}
	return json.Marshal(c)
}

type createCaseTool struct{ store *domain.Store }
type createCaseInput struct {
	InitialFields    json.RawMessage `json:"initial_fields"`
	SourceMessageIDs []string        `json:"source_message_ids"`
}

func (t *createCaseTool) Name() string { return "create_case" }
func (t *createCaseTool) Description() string {
	return "Explicitly create and select a new case for a clearly unrelated service problem."
}
func (t *createCaseTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"initial_fields":{"type":"object","properties":{"address":{"type":"string"},"issue":{"type":"string"},"urgency":{"type":"string","enum":["low","normal","high","emergency"]}},"additionalProperties":false},"source_message_ids":{"type":"array","items":{"type":"string"},"minItems":1}},"required":["initial_fields","source_message_ids"],"additionalProperties":false}`)
}
func (t *createCaseTool) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in createCaseInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	runID, _, err := runAndThread(ctx, t.store)
	if err != nil {
		return nil, err
	}
	c, err := t.store.CreateCaseForRun(ctx, runID, in.InitialFields, in.SourceMessageIDs)
	if err != nil {
		return nil, err
	}
	return json.Marshal(c)
}

// --- escalate ---

// escalateTool lets the agent summon a human to the run *now* — distinct from
// proposing a customer-facing action. It is orthogonal to approval: it does
// not decide whether an action executes (the policy still does), only how
// urgently a dispatcher engages. See design/001-escalation.md.
type escalateTool struct {
	store    *domain.Store
	notifier notify.Notifier // nil when no notification path is configured
}

type escalateInput struct {
	Reason   string `json:"reason"`
	Category string `json:"category"`
}

func (t *escalateTool) Name() string { return "escalate" }

// Description must stay honest about what escalating actually does — the
// model calibrates its safety behavior on this claim (OVERVIEW §6.3 #13).
// Only say "pages the dispatcher" when a notifier is really wired.
func (t *escalateTool) Description() string {
	reach := "This flags the conversation for urgent attention at the top of the dispatcher's queue (no page is sent, so they see it when they next check)."
	return "Flag this conversation for urgent human attention now and stand down. " + reach + " It sends neither a customer message nor an external notification. Raising it needs no approval."
}

func (t *escalateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"reason": {"type": "string", "description": "One line for the dispatcher: what's happening and what you've told the customer."},
			"category": {"type": "string", "enum": ["emergency", "stuck", "other"], "description": "Why you're escalating. Use emergency for danger to people or property."}
		},
		"required": ["reason", "category"],
		"additionalProperties": false
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
	newlyFlagged, err := t.store.RaiseEscalation(ctx, conv.ID, in.Reason)
	if err != nil {
		return nil, fmt.Errorf("escalate: %w", err)
	}
	// Best-effort delivery on the not-flagged → flagged transition only: a
	// failed page degrades to flagged-in-UI (the projection is already set),
	// and failing the action would retry into newlyFlagged=false — no re-page
	// anyway, just a spurious tool error in the agent's context.
	if newlyFlagged && t.notifier != nil { // notification support retained but disabled by Definition
		e := notify.Escalation{
			OrgID:          conv.OrgID,
			ConversationID: conv.ID,
			Reason:         in.Reason,
			Source:         "escalate_tool",
		}
		if cust, err := t.store.GetCustomer(ctx, conv.CustomerID); err == nil {
			e.CustomerName = cust.Name
		}
		if err := t.notifier.Notify(ctx, e); err != nil {
			log.Printf("escalate: notification failed for conversation %s: %v", conv.ID, err)
		}
	}
	return json.RawMessage(`{"status":"escalated"}`), nil
}
