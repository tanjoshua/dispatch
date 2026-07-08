package intake

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dispatch/agentkit"
	"dispatch/agentkit/llm"
	"dispatch/agentkit/temporalkit"
)

// TestIntakePolicyRouting pins the v1 approval policy: internal, reversible
// record-keeping auto-approves; anything customer-facing or terminal needs a
// human, and unknown tools fall back to the safe default.
func TestIntakePolicyRouting(t *testing.T) {
	policy := Definition("test-model", nil, nil).Policy
	cases := map[string]agentkit.PolicyDecision{
		"update_case":  agentkit.AutoApprove,
		"escalate":     agentkit.AutoApprove, // raising an alarm needs no permission
		"send_message": agentkit.RequireApproval,
		"close_case":   agentkit.RequireApproval,
		"unknown_tool": agentkit.RequireApproval, // default is the safe one
	}
	for tool, want := range cases {
		if got := policy.Evaluate(context.Background(), agentkit.Action{Tool: tool}); got != want {
			t.Errorf("policy for %q = %s, want %s", tool, got, want)
		}
	}
}

// TestScriptedRevisesAfterRejection is the reject→revise path asserted from a
// structured rejected Action: the pipeline renders the rejection with the same
// RejectionFeedback the loop uses, and the agent must recognize it and propose
// a *revised* customer reply — not repeat, not yield.
func TestScriptedRevisesAfterRejection(t *testing.T) {
	rejected := agentkit.Action{
		Tool:     "send_message",
		State:    agentkit.ActionRejected,
		Decision: &agentkit.Decision{Kind: agentkit.DecisionReject, Reason: "Too formal — be warmer"},
	}
	// Exactly what the loop feeds back to the agent for this rejection.
	feedbackContent := temporalkit.RejectionFeedback(rejected.Decision.Reason)

	msgs := []llm.Message{
		llm.UserText("My kitchen sink is leaking"),
		assistantToolUse(t, "call-a", "send_message", map[string]any{
			"message": "Thanks for reaching out! Could I get your name and address?",
		}),
		llm.ToolResults(llm.ToolResult{ToolCallID: "call-a", Content: feedbackContent}),
	}

	resp, err := ScriptedLLM{}.Complete(context.Background(), llm.CompletionRequest{Messages: msgs})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "send_message" {
		t.Fatalf("after rejection want one send_message, got %+v", calls)
	}
	var out struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(calls[0].Input, &out); err != nil {
		t.Fatalf("unmarshal revised message: %v", err)
	}
	// The revise branch rephrases; a repeat would carry the original text.
	if !strings.Contains(strings.ToLower(out.Message), "rephrase") {
		t.Errorf("revised reply should differ from the rejected one, got %q", out.Message)
	}
}

// TestScriptedYieldsAfterApproval is the discriminating negative: an ordinary
// (non-rejection) tool result must not trip the rejection path. If it did,
// recognition would be firing on the wrong messages — so this proves the
// detection is specific, not just present.
func TestScriptedYieldsAfterApproval(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText("My kitchen sink is leaking"),
		assistantToolUse(t, "call-a", "send_message", map[string]any{
			"message": "Got it — what's the service address?",
		}),
		llm.ToolResults(llm.ToolResult{ToolCallID: "call-a", Content: `{"status":"sent"}`}),
	}

	resp, err := ScriptedLLM{}.Complete(context.Background(), llm.CompletionRequest{Messages: msgs})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if calls := resp.ToolCalls(); len(calls) != 0 {
		t.Fatalf("after a normal result the agent should yield, got calls %+v", calls)
	}
	if resp.StopReason != llm.StopEndTurn {
		t.Errorf("stop reason = %s, want %s", resp.StopReason, llm.StopEndTurn)
	}
}

func assistantToolUse(t *testing.T, id, name string, input map[string]any) llm.Message {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: "tool_use", ToolCall: &llm.ToolCall{ID: id, Name: name, Input: raw}},
	}}
}
