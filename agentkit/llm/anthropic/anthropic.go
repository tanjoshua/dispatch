// Package anthropic adapts the official anthropic-sdk-go to agentkit's
// provider-agnostic llm.LLM interface.
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"dispatch/agentkit/llm"
)

// DefaultModel is the recommended model for agent workloads.
const DefaultModel = "claude-opus-4-8"

type Client struct {
	client sdk.Client
}

// New builds a client. With no options it reads ANTHROPIC_API_KEY from the
// environment.
func New(opts ...option.RequestOption) *Client {
	return &Client{client: sdk.NewClient(opts...)}
}

func (c *Client) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	params := sdk.MessageNewParams{
		Model:     sdk.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
	}
	if req.System != "" {
		params.System = []sdk.TextBlockParam{{Text: req.System}}
	}

	for _, t := range req.Tools {
		tool, err := toSDKTool(t)
		if err != nil {
			return nil, err
		}
		params.Tools = append(params.Tools, sdk.ToolUnionParam{OfTool: tool})
	}

	for _, m := range req.Messages {
		msg, err := toSDKMessage(m)
		if err != nil {
			return nil, err
		}
		params.Messages = append(params.Messages, msg)
	}

	resp, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	return fromSDKResponse(resp), nil
}

func toSDKTool(t llm.ToolDef) (*sdk.ToolParam, error) {
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
		return nil, fmt.Errorf("anthropic: tool %s: invalid input schema: %w", t.Name, err)
	}
	var inputSchema sdk.ToolInputSchemaParam
	// Typed fields for the keywords the SDK models; every other keyword
	// (additionalProperties, enum refinements, etc.) passes through untouched —
	// the schema is the contract, and silently dropping constraints here would
	// let the model produce input the ExecuteAction validator then rejects.
	for key, raw := range schema {
		switch key {
		case "type":
			// constant "object", implied by the SDK
		case "properties":
			var props map[string]any
			if err := json.Unmarshal(raw, &props); err != nil {
				return nil, fmt.Errorf("anthropic: tool %s: invalid properties: %w", t.Name, err)
			}
			inputSchema.Properties = props
		case "required":
			var req []string
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, fmt.Errorf("anthropic: tool %s: invalid required: %w", t.Name, err)
			}
			if len(req) > 0 {
				inputSchema.Required = req
			}
		default:
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, fmt.Errorf("anthropic: tool %s: invalid schema key %q: %w", t.Name, key, err)
			}
			if inputSchema.ExtraFields == nil {
				inputSchema.ExtraFields = map[string]any{}
			}
			inputSchema.ExtraFields[key] = v
		}
	}
	return &sdk.ToolParam{
		Name:        t.Name,
		Description: sdk.String(t.Description),
		InputSchema: inputSchema,
	}, nil
}

func toSDKMessage(m llm.Message) (sdk.MessageParam, error) {
	var blocks []sdk.ContentBlockParamUnion
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			blocks = append(blocks, sdk.NewTextBlock(b.Text))
		case "tool_use":
			if b.ToolCall == nil {
				return sdk.MessageParam{}, fmt.Errorf("anthropic: tool_use block without tool call")
			}
			blocks = append(blocks, sdk.ContentBlockParamUnion{
				OfToolUse: &sdk.ToolUseBlockParam{
					ID:    b.ToolCall.ID,
					Name:  b.ToolCall.Name,
					Input: b.ToolCall.Input,
				},
			})
		case "tool_result":
			if b.ToolResult == nil {
				return sdk.MessageParam{}, fmt.Errorf("anthropic: tool_result block without result")
			}
			blocks = append(blocks, sdk.NewToolResultBlock(b.ToolResult.ToolCallID, b.ToolResult.Content, b.ToolResult.IsError))
		default:
			return sdk.MessageParam{}, fmt.Errorf("anthropic: unsupported content block type %q", b.Type)
		}
	}
	role := sdk.MessageParamRoleUser
	if m.Role == llm.RoleAssistant {
		role = sdk.MessageParamRoleAssistant
	}
	return sdk.MessageParam{Role: role, Content: blocks}, nil
}

func fromSDKResponse(resp *sdk.Message) *llm.CompletionResponse {
	out := &llm.CompletionResponse{
		StopReason: mapStopReason(resp.StopReason),
		Usage: llm.Usage{
			InputTokens:  int(resp.Usage.InputTokens),
			OutputTokens: int(resp.Usage.OutputTokens),
		},
	}
	for _, block := range resp.Content {
		switch v := block.AsAny().(type) {
		case sdk.TextBlock:
			out.Content = append(out.Content, llm.ContentBlock{Type: "text", Text: v.Text})
		case sdk.ToolUseBlock:
			out.Content = append(out.Content, llm.ContentBlock{
				Type: "tool_use",
				ToolCall: &llm.ToolCall{
					ID:    v.ID,
					Name:  v.Name,
					Input: json.RawMessage(v.JSON.Input.Raw()),
				},
			})
		}
	}
	return out
}

func mapStopReason(r sdk.StopReason) llm.StopReason {
	switch r {
	case sdk.StopReasonEndTurn:
		return llm.StopEndTurn
	case sdk.StopReasonToolUse:
		return llm.StopToolUse
	case sdk.StopReasonMaxTokens:
		return llm.StopMaxTokens
	case sdk.StopReasonRefusal:
		return llm.StopRefusal
	default:
		return llm.StopOther
	}
}
