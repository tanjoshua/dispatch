// Package agentkit provides business-agnostic primitives for running agents
// whose tool calls are explicit, reviewable Actions: an agent proposes an
// Action, a Policy decides whether it needs human approval, a decision is
// recorded, approved actions execute, and everything lands in an append-only
// event log.
//
// agentkit knows nothing about any particular application domain. Applications
// implement Tool, choose a Policy, and hand both to the agent loop in
// temporalkit.
package agentkit

import (
	"encoding/json"
	"time"
)

// ActionState is the lifecycle state of an Action.
//
//	                 ┌── policy: auto ──────────────┐
//	proposed ── policy ──► pending_approval ──► approved ──► executing ──► completed
//	                              │                                  └───► failed
//	                              ├──► approved_with_edits ──► executing ...
//	                              └──► rejected
type ActionState string

const (
	ActionProposed          ActionState = "proposed"
	ActionPendingApproval   ActionState = "pending_approval"
	ActionApproved          ActionState = "approved"
	ActionApprovedWithEdits ActionState = "approved_with_edits"
	ActionRejected          ActionState = "rejected"
	ActionExecuting         ActionState = "executing"
	ActionCompleted         ActionState = "completed"
	ActionFailed            ActionState = "failed"
)

// DecisionKind is how an action was decided.
type DecisionKind string

const (
	DecisionApprove          DecisionKind = "approve"
	DecisionApproveWithEdits DecisionKind = "approve_with_edits"
	DecisionReject           DecisionKind = "reject"
	// DecisionDismiss drops a proposed action without sending it and without
	// asking the agent to try again now: the agent stands down for this turn
	// and re-engages on the next inbound message. Like reject, the action ends
	// unexecuted (ActionRejected state); unlike reject, no reason is required
	// and the agent does not immediately re-propose.
	DecisionDismiss DecisionKind = "dismiss"
)

// DecidedByPolicy is the DecidedBy value for policy auto-approvals.
const DecidedByPolicy = "policy:auto"

// Decision records who/what decided an action, and why.
type Decision struct {
	Kind      DecisionKind `json:"kind"`
	DecidedBy string       `json:"decided_by"` // user ID, or DecidedByPolicy
	Reason    string       `json:"reason"`     // required for reject; optional for dismiss; free text otherwise
}

// Action is one proposed tool call with its full review lifecycle.
//
// Invariants:
//   - Every tool execution flows through an Action — there is no side door.
//   - Input is what the agent proposed and is never overwritten; human edits
//     go in EditedInput.
//   - Rejections require a reason; decisions and results are fed back into
//     the agent's context.
type Action struct {
	ID          string          `json:"id"`
	OrgID       string          `json:"org_id"`
	RunID       string          `json:"run_id"`
	ToolCallID  string          `json:"tool_call_id"` // LLM-assigned ID for the tool call
	Tool        string          `json:"tool"`
	Input       json.RawMessage `json:"input"`
	EditedInput json.RawMessage `json:"edited_input,omitempty"`
	State       ActionState     `json:"state"`
	Decision    *Decision       `json:"decision,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	ProposedAt  time.Time       `json:"proposed_at"`
	DecidedAt   *time.Time      `json:"decided_at,omitempty"`
	ExecutedAt  *time.Time      `json:"executed_at,omitempty"`
}

// EffectiveInput is what should actually execute: the human-edited input when
// present, otherwise the agent's original proposal.
func (a *Action) EffectiveInput() json.RawMessage {
	if len(a.EditedInput) > 0 {
		return a.EditedInput
	}
	return a.Input
}
