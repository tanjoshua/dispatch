package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool is a capability an agent can invoke. agentkit defines the interface;
// applications implement it.
//
// Execute must only ever be called from the action pipeline (see
// temporalkit) — never directly. That invariant is what makes the audit
// trail trustworthy.
type Tool interface {
	Name() string
	Description() string          // shown to the LLM
	InputSchema() json.RawMessage // JSON Schema; the LLM is constrained to it
	Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
}

// ToolSet is a named collection of tools.
type ToolSet map[string]Tool

// NewToolSet builds a ToolSet, panicking on duplicate names (a wiring bug).
func NewToolSet(tools ...Tool) ToolSet {
	ts := make(ToolSet, len(tools))
	for _, t := range tools {
		if _, dup := ts[t.Name()]; dup {
			panic(fmt.Sprintf("agentkit: duplicate tool %q", t.Name()))
		}
		ts[t.Name()] = t
	}
	return ts
}

// runContextKey carries RunContext through Execute's ctx.
type runContextKey struct{}

// RunContext identifies the run and action on whose behalf a tool executes.
// The action pipeline injects it; tools that need to know their run (e.g. to
// look up domain state tied to it) extract it with RunContextFrom.
//
// ActionID doubles as the idempotency root for external effects: an activity
// retry re-executes the tool under the same action, so a tool that derives
// its external effect's ID from ActionID (e.g. an outbound message ID, passed
// to the provider as an idempotency key) delivers at most once.
type RunContext struct {
	RunID           string
	OrgID           string
	ActionID        string
	ModelTurnID     string
	ContextRevision int64
	EventToSeq      int64
}

// WithRunContext returns ctx carrying rc.
func WithRunContext(ctx context.Context, rc RunContext) context.Context {
	return context.WithValue(ctx, runContextKey{}, rc)
}

// RunContextFrom extracts the RunContext injected by the action pipeline.
func RunContextFrom(ctx context.Context) (RunContext, bool) {
	rc, ok := ctx.Value(runContextKey{}).(RunContext)
	return rc, ok
}
