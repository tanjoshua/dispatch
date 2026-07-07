package agentkit

import "context"

// PolicyDecision is what a Policy says about a proposed action.
type PolicyDecision int

const (
	// RequireApproval routes the action to a human. This is the safe default.
	RequireApproval PolicyDecision = iota
	// AutoApprove lets the action execute without a human decision. A
	// Decision record (DecidedBy = DecidedByPolicy) is still written.
	AutoApprove
	// Forbid rejects the action outright; the rejection is fed back to the
	// agent like a human rejection.
	Forbid
)

func (d PolicyDecision) String() string {
	switch d {
	case AutoApprove:
		return "auto_approve"
	case Forbid:
		return "forbid"
	default:
		return "require_approval"
	}
}

// Policy decides whether a proposed action needs human approval. Human-in-
// the-loop is policy, not architecture: autonomy increases by changing the
// Policy, never by restructuring the agent loop.
type Policy interface {
	Evaluate(ctx context.Context, a Action) PolicyDecision
}

// StaticPolicy maps tool name → decision, with RequireApproval as the
// default for unlisted tools. The v1 implementation; confidence scores and
// learned policies land behind the same interface later.
type StaticPolicy struct {
	ByTool map[string]PolicyDecision
}

func (p StaticPolicy) Evaluate(_ context.Context, a Action) PolicyDecision {
	if d, ok := p.ByTool[a.Tool]; ok {
		return d
	}
	return RequireApproval
}
