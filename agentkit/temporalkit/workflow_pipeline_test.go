package temporalkit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"dispatch/agentkit"
	"dispatch/agentkit/llm"
)

// These tests run the real AgentLoop workflow against the real Activities —
// scripted LLM, real tools, in-memory store with Postgres-equivalent
// idempotency — inside Temporal's test environment. They cover the pipeline
// claims OVERVIEW §6.3 #16 called untested: decision races, signal dedupe,
// proposal-preservation under edits, the turn budget, and terminal runs.

// scriptLLM plays a per-completion script and records every request.
type scriptLLM struct {
	mu     sync.Mutex
	calls  int
	reqs   []llm.CompletionRequest
	script func(call int, req llm.CompletionRequest) *llm.CompletionResponse
}

func (s *scriptLLM) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.reqs = append(s.reqs, req)
	return s.script(s.calls, req), nil
}

func (s *scriptLLM) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *scriptLLM) request(i int) llm.CompletionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[i]
}

func toolUse(id, name, input string) *llm.CompletionResponse {
	return &llm.CompletionResponse{
		Content: []llm.ContentBlock{{Type: "tool_use", ToolCall: &llm.ToolCall{
			ID: id, Name: name, Input: json.RawMessage(input)}}},
		StopReason: llm.StopToolUse,
	}
}

func textOnly(text string) *llm.CompletionResponse {
	return &llm.CompletionResponse{
		Content:    []llm.ContentBlock{{Type: "text", Text: text}},
		StopReason: llm.StopEndTurn,
	}
}

// testTool is a minimal tool; execution is recorded so tests can assert the
// pipeline actually ran it (or didn't).
type testTool struct {
	name     string
	mu       sync.Mutex
	executed []json.RawMessage
}

func (t *testTool) Name() string        { return t.name }
func (t *testTool) Description() string { return "test tool " + t.name }
func (t *testTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"note": {"type": "string"}},
		"additionalProperties": false
	}`)
}
func (t *testTool) Execute(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.executed = append(t.executed, input)
	return json.RawMessage(`{"ok":true}`), nil
}
func (t *testTool) executions() []json.RawMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]json.RawMessage(nil), t.executed...)
}

type pipelineFixture struct {
	env    *testsuite.TestWorkflowEnvironment
	ms     *memStore
	llm    *scriptLLM
	act    *testTool // requires approval
	loop   *testTool // auto-approved, non-terminal
	done   *testTool // auto-approved, terminal
	budget *int      // TurnBudgetExceeded hook invocations
	input  AgentLoopInput
}

func newPipelineFixture(t *testing.T, script func(call int, req llm.CompletionRequest) *llm.CompletionResponse) *pipelineFixture {
	t.Helper()
	f := &pipelineFixture{
		ms:     newMemStore(),
		llm:    &scriptLLM{script: script},
		act:    &testTool{name: "act"},
		loop:   &testTool{name: "loop"},
		done:   &testTool{name: "done"},
		budget: new(int),
	}
	def := AgentDefinition{
		Name:      "testagent",
		Model:     "test-model",
		System:    "You are a test agent.",
		MaxTokens: 128,
		Tools:     agentkit.NewToolSet(f.act, f.loop, f.done),
		Policy: agentkit.StaticPolicy{ByTool: map[string]agentkit.PolicyDecision{
			"act":  agentkit.RequireApproval,
			"loop": agentkit.AutoApprove,
			"done": agentkit.AutoApprove,
		}},
		TerminalTools: []string{"done"},
	}
	acts := &Activities{
		LLM:    f.llm,
		Store:  f.ms,
		Agents: map[string]AgentDefinition{def.Name: def},
		TurnBudgetExceeded: func(_ context.Context, _, _ string) error {
			*f.budget++
			return nil
		},
	}
	f.input = AgentLoopInput{RunID: "run1", OrgID: "org1", Agent: def.Name}
	_ = f.ms.CreateRun(context.Background(), agentkit.Run{
		ID: f.input.RunID, OrgID: f.input.OrgID, Agent: def.Name, Status: agentkit.RunRunning,
	})

	var ts testsuite.WorkflowTestSuite
	f.env = ts.NewTestWorkflowEnvironment()
	f.env.RegisterWorkflowWithOptions(AgentLoopWorkflow, workflow.RegisterOptions{Name: AgentLoopWorkflowName})
	f.env.RegisterActivity(acts)
	return f
}

func (f *pipelineFixture) inboundAt(d time.Duration, id, text string) {
	f.env.RegisterDelayedCallback(func() {
		f.env.SignalWorkflow(SignalInboundMessage, InboundMessage{MessageID: id, Text: text})
	}, d)
}

func (f *pipelineFixture) run(t *testing.T) {
	t.Helper()
	f.env.ExecuteWorkflow(AgentLoopWorkflowName, f.input)
	if !f.env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := f.env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
}

// TestPipelineApprovalWithEditsPreservesProposal drives propose → human
// approve-with-edits → execute → terminal, and asserts the hard rules: the
// tool ran with the edited input, the agent's original proposal survived
// untouched, and the run completed with the full event trail.
func TestPipelineApprovalWithEditsPreservesProposal(t *testing.T) {
	f := newPipelineFixture(t, func(call int, _ llm.CompletionRequest) *llm.CompletionResponse {
		switch call {
		case 1:
			return toolUse("c1", "act", `{"note":"original"}`)
		default:
			return toolUse("c2", "done", `{}`)
		}
	})
	f.inboundAt(time.Millisecond, "m1", "hello")
	f.env.RegisterDelayedCallback(func() {
		id := f.ms.pendingActionID("run1")
		if id == "" {
			t.Error("no pending action to decide")
			return
		}
		f.env.SignalWorkflow(SignalDecision, DecisionSignal{
			ActionID:    id,
			Kind:        agentkit.DecisionApproveWithEdits,
			DecidedBy:   "tester",
			EditedInput: json.RawMessage(`{"note":"edited"}`),
		})
	}, time.Second)
	f.run(t)

	act := f.ms.actionByTool("run1", "act")
	if act == nil {
		t.Fatal("act action not recorded")
	}
	if act.State != agentkit.ActionCompleted {
		t.Errorf("act state = %s, want completed", act.State)
	}
	if string(act.Input) != `{"note":"original"}` {
		t.Errorf("original proposal overwritten: %s", act.Input)
	}
	if string(act.EditedInput) != `{"note":"edited"}` {
		t.Errorf("edited input = %s", act.EditedInput)
	}
	execs := f.act.executions()
	if len(execs) != 1 || string(execs[0]) != `{"note":"edited"}` {
		t.Errorf("tool executed with %v, want the edited input exactly once", execs)
	}
	if run, _ := f.ms.GetRun(context.Background(), "org1", "run1"); run.Status != agentkit.RunCompleted {
		t.Errorf("run status = %s, want completed", run.Status)
	}
	for _, typ := range []agentkit.EventType{
		agentkit.EventActionProposed, agentkit.EventDecisionMade,
		agentkit.EventActionExecuted, agentkit.EventRunCompleted, agentkit.EventLLMCompleted,
	} {
		if len(f.ms.eventsOfType("run1", typ)) == 0 {
			t.Errorf("no %s event recorded", typ)
		}
	}
}

// TestPipelineRedeliveredInboundSignalDoesNotRetrigger: the same message ID
// signaled twice (webhook retry after the run already turned) must not pay
// for another LLM turn.
func TestPipelineRedeliveredInboundSignalDoesNotRetrigger(t *testing.T) {
	f := newPipelineFixture(t, func(call int, _ llm.CompletionRequest) *llm.CompletionResponse {
		if call == 1 {
			return textOnly("noted, waiting")
		}
		return toolUse("c-done", "done", `{}`)
	})
	f.inboundAt(time.Millisecond, "m1", "first")
	f.inboundAt(500*time.Millisecond, "m1", "first") // redelivery
	f.inboundAt(time.Second, "m2", "wrap it up")
	f.run(t)

	if got := f.llm.callCount(); got != 2 {
		t.Errorf("LLM calls = %d, want 2 (one per distinct message; redelivery must not trigger a turn)", got)
	}
}

// TestPipelineLateDecisionRecordedAsDropped: a decision arriving when nothing
// is pending (supersede race, second dispatcher) is recorded on the audit
// trail instead of vanishing.
func TestPipelineLateDecisionRecordedAsDropped(t *testing.T) {
	f := newPipelineFixture(t, func(call int, _ llm.CompletionRequest) *llm.CompletionResponse {
		return toolUse("c-done", "done", `{}`)
	})
	f.env.RegisterDelayedCallback(func() {
		f.env.SignalWorkflow(SignalDecision, DecisionSignal{
			ActionID: "act_stale", Kind: agentkit.DecisionApprove, DecidedBy: "late-dispatcher",
		})
	}, time.Millisecond)
	f.inboundAt(time.Second, "m1", "hello")
	f.run(t)

	drops := f.ms.eventsOfType("run1", agentkit.EventDecisionDropped)
	if len(drops) != 1 {
		t.Fatalf("decision_dropped events = %d, want 1", len(drops))
	}
	var payload map[string]any
	_ = json.Unmarshal(drops[0].Payload, &payload)
	if payload["action_id"] != "act_stale" || payload["decided_by"] != "late-dispatcher" {
		t.Errorf("dropped payload = %v", payload)
	}
}

// TestPipelineRejectionFeedsBack: a rejection resolves the action unexecuted
// and its reason reaches the agent's next completion as tool-result feedback.
func TestPipelineRejectionFeedsBack(t *testing.T) {
	f := newPipelineFixture(t, func(call int, _ llm.CompletionRequest) *llm.CompletionResponse {
		if call == 1 {
			return toolUse("c1", "act", `{"note":"draft"}`)
		}
		return toolUse("c2", "done", `{}`)
	})
	f.inboundAt(time.Millisecond, "m1", "hello")
	f.env.RegisterDelayedCallback(func() {
		id := f.ms.pendingActionID("run1")
		if id == "" {
			t.Error("no pending action to reject")
			return
		}
		f.env.SignalWorkflow(SignalDecision, DecisionSignal{
			ActionID: id, Kind: agentkit.DecisionReject, DecidedBy: "tester", Reason: "too vague",
		})
	}, time.Second)
	f.run(t)

	act := f.ms.actionByTool("run1", "act")
	if act.State != agentkit.ActionRejected {
		t.Errorf("act state = %s, want rejected", act.State)
	}
	if len(f.act.executions()) != 0 {
		t.Error("rejected action was executed — side door around the pipeline")
	}
	// The second completion's request must carry the rejection as feedback.
	req := f.llm.request(1)
	last := req.Messages[len(req.Messages)-1]
	found := false
	for _, b := range last.Content {
		if b.Type == "tool_result" && b.ToolResult != nil &&
			IsRejectionFeedback(b.ToolResult.Content) &&
			strings.Contains(b.ToolResult.Content, "too vague") {
			found = true
		}
	}
	if !found {
		t.Errorf("rejection feedback missing from next completion: %+v", last)
	}
}

// TestPipelineTurnBudgetStopsRunawayTurn: an agent that keeps proposing
// auto-approved tools is stopped at MaxLLMCallsPerTurn, the event and app
// hook fire, and the run survives to serve the next message.
func TestPipelineTurnBudgetStopsRunawayTurn(t *testing.T) {
	f := newPipelineFixture(t, func(call int, _ llm.CompletionRequest) *llm.CompletionResponse {
		if call <= MaxLLMCallsPerTurn {
			return toolUse(fmt.Sprintf("c%d", call), "loop", `{}`)
		}
		return toolUse("c-done", "done", `{}`)
	})
	f.inboundAt(time.Millisecond, "m1", "go")
	f.inboundAt(time.Second, "m2", "still there?")
	f.run(t)

	if got := len(f.ms.eventsOfType("run1", agentkit.EventTurnBudgetExceeded)); got != 1 {
		t.Errorf("turn_budget_exceeded events = %d, want 1", got)
	}
	if *f.budget != 1 {
		t.Errorf("budget hook invocations = %d, want 1", *f.budget)
	}
	if got := len(f.loop.executions()); got != MaxLLMCallsPerTurn {
		t.Errorf("loop executions = %d, want %d (budget stops the turn)", got, MaxLLMCallsPerTurn)
	}
	if run, _ := f.ms.GetRun(context.Background(), "org1", "run1"); run.Status != agentkit.RunCompleted {
		t.Errorf("run status = %s, want completed (second turn finished the run)", run.Status)
	}
}

// TestPipelineDismissStandsDown: a dismissed draft ends the turn without
// execution and without a revision loop.
func TestPipelineDismissStandsDown(t *testing.T) {
	f := newPipelineFixture(t, func(call int, _ llm.CompletionRequest) *llm.CompletionResponse {
		if call == 1 {
			return toolUse("c1", "act", `{"note":"draft"}`)
		}
		return toolUse("c-done", "done", `{}`)
	})
	f.inboundAt(time.Millisecond, "m1", "hello")
	f.env.RegisterDelayedCallback(func() {
		id := f.ms.pendingActionID("run1")
		if id == "" {
			t.Error("no pending action to dismiss")
			return
		}
		f.env.SignalWorkflow(SignalDecision, DecisionSignal{
			ActionID: id, Kind: agentkit.DecisionDismiss, DecidedBy: "tester",
		})
	}, time.Second)
	f.inboundAt(2*time.Second, "m2", "actually, all done")
	f.run(t)

	if len(f.act.executions()) != 0 {
		t.Error("dismissed action was executed")
	}
	// One completion for the dismissed turn, one for the wrap-up turn: the
	// dismiss must not spin a revision loop.
	if got := f.llm.callCount(); got != 2 {
		t.Errorf("LLM calls = %d, want 2 (dismiss stands the agent down)", got)
	}
}
