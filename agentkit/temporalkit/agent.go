// Package temporalkit implements agentkit's agent loop as a Temporal
// workflow pattern: durable waits on inbound messages and human decisions,
// with every non-deterministic step (LLM calls, DB writes, tool execution,
// ID generation) pushed into activities.
package temporalkit

import (
	"encoding/json"
	"sort"

	"dispatch/agentkit"
	"dispatch/agentkit/llm"
)

// Signal names. Signals carry IDs and small payloads, never blobs.
const (
	SignalInboundMessage = "inbound_message"
	SignalDecision       = "decision"
	// SignalDispatcherMessage carries a message a human participant (the
	// dispatcher) sent directly to the customer, so the agent's shared context
	// includes it (design/003-dispatcher-as-participant.md). The message is
	// already delivered and persisted before the signal fires; the payload is
	// just id + text, keeping the signal small.
	SignalDispatcherMessage = "dispatcher_message"
)

// AgentDefinition is everything the loop needs to run one kind of agent:
// prompt, tool set, policy, model. Applications register definitions with
// the worker's Activities; workflows reference them by name only.
type AgentDefinition struct {
	Name      string
	Model     string
	System    string
	MaxTokens int
	Tools     agentkit.ToolSet
	Policy    agentkit.Policy
	// TerminalTools names tools whose successful execution completes the
	// run (e.g. an intake agent's close_case).
	TerminalTools []string
}

func (d AgentDefinition) isTerminal(tool string) bool {
	for _, t := range d.TerminalTools {
		if t == tool {
			return true
		}
	}
	return false
}

// toolDefs renders the tool set for the LLM, sorted by name so the request
// is deterministic (stable prompt-cache prefix).
func (d AgentDefinition) toolDefs() []llm.ToolDef {
	names := make([]string, 0, len(d.Tools))
	for name := range d.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	defs := make([]llm.ToolDef, 0, len(names))
	for _, name := range names {
		t := d.Tools[name]
		defs = append(defs, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return defs
}

// AgentLoopInput starts (or continues, after ContinueAsNew) an agent run.
type AgentLoopInput struct {
	RunID string `json:"run_id"`
	OrgID string `json:"org_id"`
	Agent string `json:"agent"` // agent definition name

	// Messages carries accumulated conversation context across
	// ContinueAsNew. Empty on a fresh run.
	Messages []llm.Message `json:"messages,omitempty"`

	// ProcessedMessageIDs carries the IDs of customer/dispatcher messages
	// already absorbed into Messages. Channel adapters re-signal on duplicate
	// deliveries (webhook retries), so the workflow dedupes signals by message
	// ID — each external message drives at most one turn. Append-ordered
	// (deterministic under replay); carried across ContinueAsNew like Messages.
	ProcessedMessageIDs []string `json:"processed_message_ids,omitempty"`
}

// InboundMessage is the inbound_message signal payload. Channel adapters
// persist the message and its event first, then signal.
type InboundMessage struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

// DispatcherMessageSignal is the dispatcher_message signal payload: a message a
// human sent directly to the customer, carried into the run for agent context.
type DispatcherMessageSignal struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

// DecisionSignal is the decision signal payload: a human ruling on one
// pending action.
type DecisionSignal struct {
	ActionID    string                `json:"action_id"`
	Kind        agentkit.DecisionKind `json:"kind"`
	DecidedBy   string                `json:"decided_by"`
	EditedInput json.RawMessage       `json:"edited_input,omitempty"`
	Reason      string                `json:"reason,omitempty"`
}
