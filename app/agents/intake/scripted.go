package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dispatch/agentkit/llm"
	"dispatch/agentkit/temporalkit"
)

// unfence strips the ExternalText delimiters a real model is told to treat as
// data boundaries, so the script keys on the customer's actual words.
func unfence(text string) string {
	open, close := "<"+temporalkit.ExternalMessageTag+">", "</"+temporalkit.ExternalMessageTag+">"
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, open)
	text = strings.TrimSuffix(text, close)
	return strings.TrimSpace(text)
}

// ScriptedLLM is a deterministic stand-in for a real model, used for demos
// and end-to-end tests without an API key (DISPATCH_FAKE_LLM=1). It walks a
// fixed intake script keyed on how many customer messages it has seen, and
// honors the tool-result protocol (including revising after a rejection).
type ScriptedLLM struct{}

func (ScriptedLLM) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	userTurns := 0
	lastText := ""
	toolCalls := 0
	for _, m := range req.Messages {
		for _, b := range m.Content {
			switch {
			case m.Role == llm.RoleUser && b.Type == "text":
				userTurns++
				lastText = unfence(b.Text)
			case m.Role == llm.RoleAssistant && b.Type == "tool_use":
				toolCalls++
			}
		}
	}

	// A rejection only matters when it's in the results of the immediately
	// preceding assistant turn — revise once, don't loop on old feedback.
	rejected := false
	if len(req.Messages) > 0 {
		for _, b := range req.Messages[len(req.Messages)-1].Content {
			if b.Type == "tool_result" && b.ToolResult != nil && temporalkit.IsRejectionFeedback(b.ToolResult.Content) {
				rejected = true
			}
		}
	}

	// After this assistant turn's tools all resolved, yield (no more calls)
	// unless we're on the closing step, which ends the run via close_case.
	lastRole := llm.RoleUser
	if len(req.Messages) > 0 {
		lastRole = req.Messages[len(req.Messages)-1].Role
	}
	hasToolResults := lastRole == llm.RoleUser && len(req.Messages) > 0 &&
		len(req.Messages[len(req.Messages)-1].Content) > 0 &&
		req.Messages[len(req.Messages)-1].Content[0].Type == "tool_result"
	if hasToolResults && !rejected {
		return &llm.CompletionResponse{
			Content:    []llm.ContentBlock{{Type: "text", Text: "(waiting for the customer)"}},
			StopReason: llm.StopEndTurn,
		}, nil
	}

	id := func(n int) string { return fmt.Sprintf("scripted-%d-%d", userTurns, toolCalls+n) }
	call := func(n int, name string, input any) llm.ContentBlock {
		raw, _ := json.Marshal(input)
		return llm.ContentBlock{Type: "tool_use", ToolCall: &llm.ToolCall{ID: id(n), Name: name, Input: raw}}
	}
	reply := func(text string) llm.ContentBlock {
		return call(0, "propose_response", map[string]any{"message": text, "responds_through_event_seq": userTurns, "after_delivery": map[string]any{"complete_run": false, "mark_intake_complete": false, "summary": ""}})
	}
	inquiryReply := func(text string) llm.ContentBlock {
		return call(0, "propose_response", map[string]any{"message": text, "responds_through_event_seq": userTurns, "after_delivery": map[string]any{"complete_run": true, "mark_intake_complete": false, "summary": ""}})
	}

	var content []llm.ContentBlock
	switch {
	case rejected:
		content = []llm.ContentBlock{reply("Sorry about that — let me rephrase. Could you tell me a bit more?")}
	// "gas" in the latest customer message plays the emergency path: escalate
	// (auto-approved, exercises the notification pipeline) plus a safety
	// message, per the real prompt's instructions.
	case strings.Contains(strings.ToLower(lastText), "gas"):
		content = []llm.ContentBlock{
			call(0, "escalate", map[string]string{
				"reason":   "Possible gas emergency — customer says: " + lastText,
				"category": "emergency",
			}),
		}
	// The deterministic demo cannot read organization config. This branch is
	// intentionally representative only; grounding is verified with a real LLM.
	case strings.Contains(strings.ToLower(lastText), "hours") || strings.Contains(strings.ToLower(lastText), "open"):
		content = []llm.ContentBlock{inquiryReply("We're open Monday–Friday, 8am–6pm, and Saturday, 9am–1pm. We're closed Sunday.")}
	case userTurns <= 1:
		content = []llm.ContentBlock{reply("Thanks for reaching out! Could I get your name and the service address?")}
	case userTurns == 2:
		content = []llm.ContentBlock{reply("Got it. How urgent is this — is it something that needs attention today?")}
	default:
		content = []llm.ContentBlock{reply("Thanks — the dispatcher has the latest details and will follow up.")}
	}
	return &llm.CompletionResponse{Content: content, StopReason: llm.StopToolUse}, nil
}
