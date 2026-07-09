package agentkit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type schemaOnlyTool struct{ schema string }

func (t schemaOnlyTool) Name() string                 { return "test_tool" }
func (t schemaOnlyTool) Description() string          { return "" }
func (t schemaOnlyTool) InputSchema() json.RawMessage { return json.RawMessage(t.schema) }
func (t schemaOnlyTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

// TestValidateToolInput pins the ExecuteAction choke-point contract: the
// effective input (agent proposal or human edit alike) must satisfy the tool's
// declared schema — including the constraints beyond properties/required that
// the LLM provider may not have enforced.
func TestValidateToolInput(t *testing.T) {
	tool := schemaOnlyTool{schema: `{
		"type": "object",
		"properties": {
			"message": {"type": "string"},
			"urgency": {"type": "string", "enum": ["low", "high"]}
		},
		"required": ["message"],
		"additionalProperties": false
	}`}

	cases := []struct {
		name    string
		input   string
		wantErr string // substring; "" means valid
	}{
		{"valid", `{"message": "hi", "urgency": "low"}`, ""},
		{"missing required", `{"urgency": "low"}`, "message"},
		{"unknown key rejected", `{"message": "hi", "extra": 1}`, "additional properties"},
		{"enum violation", `{"message": "hi", "urgency": "whenever"}`, "urgency"},
		{"wrong type", `{"message": 42}`, "message"},
		{"not json", `{"message": `, "not valid JSON"},
		{"empty input means empty object", ``, "message"}, // {} still misses required
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateToolInput(tool, json.RawMessage(tc.input))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}
