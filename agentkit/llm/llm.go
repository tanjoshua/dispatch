// Package llm defines a minimal, provider-agnostic chat-with-tools
// interface. It is deliberately the intersection of Anthropic/OpenAI-style
// tool-calling chat APIs, not the union of every provider feature —
// provider-specific capabilities belong in adapter-level options.
//
// All types round-trip JSON: they cross Temporal activity boundaries.
package llm

import (
	"context"
	"encoding/json"
)

type LLM interface {
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one conversation turn. Assistant turns may carry tool_use
// blocks; user turns carry text and/or tool_result blocks.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

type ContentBlock struct {
	Type       string      `json:"type"` // "text" | "tool_use" | "tool_result"
	Text       string      `json:"text,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`   // when Type == "tool_use"
	ToolResult *ToolResult `json:"tool_result,omitempty"` // when Type == "tool_result"
}

// ToolCall is the model proposing one tool invocation.
type ToolCall struct {
	ID    string          `json:"id"` // provider-assigned; unique within the conversation
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult feeds a tool execution outcome (or a rejection) back to the model.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

// ToolDef describes a tool to the model.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"` // JSON Schema
}

type CompletionRequest struct {
	Model     string    `json:"model"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Tools     []ToolDef `json:"tools,omitempty"`
	MaxTokens int       `json:"max_tokens"`
}

type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
	StopOther     StopReason = "other"
)

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type CompletionResponse struct {
	Content    []ContentBlock `json:"content"` // text + tool_use blocks, in order
	StopReason StopReason     `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// ToolCalls extracts the tool_use blocks in order.
func (r *CompletionResponse) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, b := range r.Content {
		if b.Type == "tool_use" && b.ToolCall != nil {
			calls = append(calls, *b.ToolCall)
		}
	}
	return calls
}

// Text concatenates the text blocks.
func (r *CompletionResponse) Text() string {
	var s string
	for _, b := range r.Content {
		if b.Type == "text" {
			s += b.Text
		}
	}
	return s
}

// UserText builds a plain-text user message.
func UserText(text string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: text}}}
}

// ToolResults builds the user message carrying tool results. All results for
// one assistant turn must go in a single message.
func ToolResults(results ...ToolResult) Message {
	m := Message{Role: RoleUser}
	for _, r := range results {
		r := r
		m.Content = append(m.Content, ContentBlock{Type: "tool_result", ToolResult: &r})
	}
	return m
}

// AssistantMessage wraps a completion response as a conversation turn.
func AssistantMessage(resp *CompletionResponse) Message {
	return Message{Role: RoleAssistant, Content: resp.Content}
}
