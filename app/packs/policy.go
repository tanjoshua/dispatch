package packs

import (
	"context"

	"dispatch/agentkit"
)

// ToolPolicy applies the fixed v1 safety policy from code-owned pack effects.
// Organization Agent Behavior config cannot alter these decisions.
type ToolPolicy struct {
	Pack Pack
}

func (p ToolPolicy) Evaluate(_ context.Context, action agentkit.Action) agentkit.PolicyDecision {
	tool, ok := p.Pack.ToolInfo(action.Tool)
	if !ok {
		return agentkit.Forbid
	}
	return decisionForEffect(tool.Effect)
}

// ConfigPolicy remains as a source-compatible name for callers while policy
// configuration is removed from the organization-facing product.
type ConfigPolicy = ToolPolicy

func decisionForEffect(effect ToolEffect) agentkit.PolicyDecision {
	switch effect {
	case EffectReadOnly, EffectControl:
		return agentkit.AutoApprove
	case EffectCustomerCommunication, EffectBusinessMutation:
		return agentkit.RequireApproval
	default:
		return agentkit.Forbid
	}
}
