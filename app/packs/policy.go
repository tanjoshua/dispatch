package packs

import (
	"context"
	"encoding/json"

	"dispatch/agentkit"
)

type ConfigPolicy struct { Pack Pack; Effective Effective }

func (p ConfigPolicy) Evaluate(_ context.Context, a agentkit.Action) agentkit.PolicyDecision {
	lane := "service_job"
	if a.Tool == "propose_response" {
		lane = "inquiry"
		var deps map[string]any
		if json.Unmarshal(a.DependencyVersions, &deps) == nil {
			if id, ok := deps["case_id"].(string); ok && id != "" { lane = "service_job" }
		}
	}
	tool, ok := p.Effective.Policy[lane][a.Tool]
	if !ok { // fixed tools may be described only in service_job
		for _, l := range p.Pack.Lanes { for _, t := range l.Tools { if t.Name == a.Tool && len(t.Settings) == 1 { return decision(t.Settings[0]) } } }
		return agentkit.RequireApproval
	}
	return decision(tool.Value)
}

func decision(v PolicyValue) agentkit.PolicyDecision {
	switch v { case Auto: return agentkit.AutoApprove; case Forbid: return agentkit.Forbid; default: return agentkit.RequireApproval }
}
