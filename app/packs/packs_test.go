package packs

import (
	"context"
	"encoding/json"
	"testing"

	"dispatch/agentkit"
)

func TestPolicyLaneAttributionAndFloor(t *testing.T) {
	p := Default()["field-service"]
	raw := json.RawMessage(`{"schema":1,"pack":"field-service","model_tier":"best","voice":{"agent_name":"D","tone":"plain"},"policy":{"inquiry":{"propose_response":"auto"},"service_job":{"propose_response":"require_review","create_case":"forbid"}}}`)
	e := EffectiveConfig(p, raw)
	policy := ConfigPolicy{Pack: p, Effective: e}
	if got := policy.Evaluate(context.Background(), agentkit.Action{Tool: "propose_response", DependencyVersions: json.RawMessage(`{}`)}); got != agentkit.AutoApprove {
		t.Fatalf("inquiry = %v", got)
	}
	if got := policy.Evaluate(context.Background(), agentkit.Action{Tool: "propose_response", DependencyVersions: json.RawMessage(`{"case_id":"c"}`)}); got != agentkit.RequireApproval {
		t.Fatalf("service = %v", got)
	}
	if got := policy.Evaluate(context.Background(), agentkit.Action{Tool: "list_candidate_cases"}); got != agentkit.AutoApprove {
		t.Fatalf("fixed = %v", got)
	}
}

func TestValidateRejectsResponseForbid(t *testing.T) {
	p := Default()["field-service"]
	raw := json.RawMessage(`{"schema":1,"pack":"field-service","model_tier":"best","voice":{"agent_name":"D"},"policy":{"inquiry":{"propose_response":"forbid"}}}`)
	if ValidateConfig(p, raw) == nil {
		t.Fatal("expected floor error")
	}
}

func TestEffectiveDefaultsAndStageOverride(t *testing.T) {
	p := Default()["field-service"]
	e := EffectiveConfig(p, json.RawMessage(`{"schema":1,"pack":"field-service","model_tier":"legacy","model_override":"ignored","models":{"per_stage":{"triage":{"override":"custom-model"}}},"voice":{"agent_name":""},"policy":{"service_job":{"create_case":"nonsense"}}}`))
	if e.Model != "custom-model" {
		t.Fatalf("model = %q", e.Model)
	}
	if e.Models["service_job"] != "claude-opus-4-8" {
		t.Fatalf("service model = %q", e.Models["service_job"])
	}
	tool := e.Policy["service_job"]["create_case"]
	if tool.Value != Auto || tool.Source != "pack_default" {
		t.Fatalf("tool = %+v", tool)
	}
}

func TestRenderPromptDegradesWithoutKnowledge(t *testing.T) {
	p := Default()["field-service"]
	e := EffectiveConfig(p, nil)
	prompt, err := RenderPrompt(p, e.Config, Profile{})
	if err != nil {
		t.Fatal(err)
	}
	if prompt == "" {
		t.Fatal("empty prompt")
	}
}
