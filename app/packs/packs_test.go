package packs

import (
	"context"
	"encoding/json"
	"testing"

	"dispatch/agentkit"
)

func TestToolPolicyUsesFixedEffectClassification(t *testing.T) {
	policy := ToolPolicy{Pack: Default()["field-service"]}
	tests := map[string]agentkit.PolicyDecision{
		"propose_response":     agentkit.RequireApproval,
		"create_case":          agentkit.RequireApproval,
		"select_case":          agentkit.RequireApproval,
		"update_case":          agentkit.RequireApproval,
		"list_candidate_cases": agentkit.AutoApprove,
		"route_intent":         agentkit.AutoApprove,
		"escalate":             agentkit.AutoApprove,
		"stand_down":           agentkit.AutoApprove,
		"wait_for_external":    agentkit.AutoApprove,
		"unknown_tool":         agentkit.Forbid,
	}
	for tool, want := range tests {
		t.Run(tool, func(t *testing.T) {
			if got := policy.Evaluate(context.Background(), agentkit.Action{Tool: tool}); got != want {
				t.Fatalf("decision = %v, want %v", got, want)
			}
		})
	}
}

func TestEffectiveConfigReadsLegacyVoiceAndIgnoresLegacyControls(t *testing.T) {
	p := Default()["field-service"]
	raw := json.RawMessage(`{
		"schema":1,
		"pack":"untrusted-pack",
		"models":{"per_stage":{"triage":{"override":"untrusted-model"}}},
		"voice":{"agent_name":"D","tone":"plain","custom_instructions":"Be brief."},
		"policy":{"inquiry":{"propose_response":"auto"},"service_job":{"create_case":"auto"}}
	}`)
	effective := EffectiveConfig(p, raw)
	if effective.Config.Schema != 2 {
		t.Fatalf("schema = %d, want 2", effective.Config.Schema)
	}
	if effective.Config.Voice.AgentName != "D" || effective.Config.Voice.Tone != "plain" || effective.Config.Voice.CustomInstructions != "Be brief." {
		t.Fatalf("voice = %+v", effective.Config.Voice)
	}
	if effective.Model != "claude-sonnet-5" {
		t.Fatalf("triage model = %q", effective.Model)
	}
	if effective.Models["service_job"] != "claude-opus-4-8" {
		t.Fatalf("service model = %q", effective.Models["service_job"])
	}
	tool := effective.Policy["service_job"]["create_case"]
	if tool.Value != RequireReview || tool.Source != "pack" || !tool.Locked {
		t.Fatalf("effective tool = %+v", tool)
	}
}

func TestValidateConfigAcceptsOnlySchemaTwoVoice(t *testing.T) {
	p := Default()["field-service"]
	valid := json.RawMessage(`{"schema":2,"voice":{"agent_name":"Dispatch","tone":"clear","custom_instructions":"Be brief."}}`)
	if err := ValidateConfig(p, valid); err != nil {
		t.Fatalf("valid config: %v", err)
	}

	invalid := []json.RawMessage{
		json.RawMessage(`{"schema":1,"voice":{"agent_name":"Dispatch","tone":"clear"}}`),
		json.RawMessage(`{"schema":2,"pack":"field-service","voice":{"agent_name":"Dispatch","tone":"clear"}}`),
		json.RawMessage(`{"schema":2,"voice":{"agent_name":"Dispatch","tone":"clear"},"policy":{}}`),
		json.RawMessage(`{"schema":2,"voice":{"agent_name":"","tone":""}}`),
		json.RawMessage(`{"schema":2,"voice":{"agent_name":"Dispatch","tone":"clear"}} {}`),
	}
	for i, raw := range invalid {
		if err := ValidateConfig(p, raw); err == nil {
			t.Errorf("invalid config %d was accepted", i)
		}
	}
}

func TestValidateStoredConfigAllowsLegacyVoiceOnlyForMigration(t *testing.T) {
	p := Default()["field-service"]
	legacy := json.RawMessage(`{"schema":1,"pack":"field-service","models":{"per_stage":{"triage":{"override":"old"}}},"voice":{"agent_name":"Dispatch","tone":"clear"},"policy":{"service_job":{"create_case":"auto"}}}`)
	if err := ValidateStoredConfig(p, legacy); err != nil {
		t.Fatalf("legacy config: %v", err)
	}
	if err := ValidateStoredConfig(p, json.RawMessage(`{"schema":1,"voice":{"agent_name":"Dispatch"}}`)); err == nil {
		t.Fatal("legacy config without a tone was accepted")
	}
}

func TestRegistryToolValidationAndFiltering(t *testing.T) {
	p := Default()["field-service"]
	available := agentkit.ToolSet{}
	for _, info := range p.Tools {
		available[info.Name] = fakeTool{name: info.Name}
	}
	if err := ValidateRegistryTools(Default(), available); err != nil {
		t.Fatalf("valid registry: %v", err)
	}
	if err := ValidateRegistryTools(Default(), agentkit.NewToolSet(fakeTool{name: "propose_response"})); err == nil {
		t.Fatal("missing pack-declared tools were accepted")
	}
	filtered := ToolSetForPack(p, agentkit.NewToolSet(fakeTool{name: "propose_response"}, fakeTool{name: "not_for_this_pack"}))
	if len(filtered) != 1 || filtered["propose_response"] == nil {
		t.Fatalf("filtered tools = %v", filtered)
	}
	if err := ValidateRegistryTools(Default(), agentkit.NewToolSet(fakeTool{name: "unclassified"})); err == nil {
		t.Fatal("unclassified registered tool was accepted")
	}

	broken := p
	broken.Tools = append([]ToolInfo(nil), p.Tools...)
	broken.Tools[0].Effect = ""
	if err := ValidateRegistryTools(Registry{broken.ID: broken}, available); err == nil {
		t.Fatal("tool without an effect was accepted")
	}
}

func TestRenderPromptDegradesWithoutKnowledge(t *testing.T) {
	p := Default()["field-service"]
	effective := EffectiveConfig(p, nil)
	prompt, err := RenderPrompt(p, effective.Config, Profile{})
	if err != nil {
		t.Fatal(err)
	}
	if prompt == "" {
		t.Fatal("empty prompt")
	}
}

type fakeTool struct {
	name string
}

func (t fakeTool) Name() string               { return t.name }
func (fakeTool) Description() string          { return "test" }
func (fakeTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (fakeTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
