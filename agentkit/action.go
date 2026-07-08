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
	// DecisionDismiss discards a proposed action: it is not sent, and the agent
	// is not asked to try again now. Like reject, the action ends unexecuted
	// (ActionRejected state); unlike reject, no reason is required and the agent
	// does not immediately re-propose — it waits for the next inbound message,
	// the same as after any completed turn. (This is a plain "not this draft",
	// not a control handoff — see design/003-dispatcher-as-participant.md.)
	DecisionDismiss DecisionKind = "dismiss"
	// DecisionSupersede withdraws a pending action because a human participant
	// acted directly instead — e.g. a dispatcher replied to the customer in
	// their own words rather than deciding on the agent's draft. The action ends
	// unexecuted (ActionRejected state); the human's act (delivered and recorded
	// separately) is what reached the customer. Not a user-supplied decision on
	// the review endpoint — the workflow records it when a direct human message
	// arrives while a draft is pending.
	DecisionSupersede DecisionKind = "supersede"
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
