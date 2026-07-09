package agentkit

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidateToolInput checks input against the tool's declared JSON Schema.
// The action pipeline calls it at the ExecuteAction choke point, so *every*
// execution path is covered with one check: the agent's proposal (providers
// mostly conform, but the schema is the contract, not a hope) and — the case
// that actually bites — a human's edited_input, which no model constrained.
// Invalid input never reaches Execute.
func ValidateToolInput(tool Tool, input json.RawMessage) error {
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(tool.InputSchema()))
	if err != nil {
		return fmt.Errorf("tool %s: invalid input schema: %w", tool.Name(), err)
	}
	// A synthetic, absolute URI: it names the schema in error messages (a
	// relative name would render as a file:// path under the process's cwd).
	url := "urn:agentkit:tool:" + tool.Name()
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(url, schemaDoc); err != nil {
		return fmt.Errorf("tool %s: invalid input schema: %w", tool.Name(), err)
	}
	schema, err := compiler.Compile(url)
	if err != nil {
		return fmt.Errorf("tool %s: invalid input schema: %w", tool.Name(), err)
	}

	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(input))
	if err != nil {
		return fmt.Errorf("input is not valid JSON: %w", err)
	}
	if err := schema.Validate(instance); err != nil {
		return fmt.Errorf("input does not match the %s schema: %w", tool.Name(), err)
	}
	return nil
}
