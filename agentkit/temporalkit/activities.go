package temporalkit

import (
	"context"
	"encoding/json"
	"fmt"

	"dispatch/agentkit"
	"dispatch/agentkit/llm"
	"dispatch/agentkit/store"
)

// Activities holds the dependencies the agent loop's activities need. The
// worker registers one instance; all non-determinism (LLM, DB, tool
// execution, IDs) lives here, never in workflow code.
type Activities struct {
	LLM    llm.LLM
	Store  store.Store
	Agents map[string]AgentDefinition
}

func (a *Activities) agent(name string) (AgentDefinition, error) {
	def, ok := a.Agents[name]
	if !ok {
		return AgentDefinition{}, fmt.Errorf("temporalkit: unknown agent %q", name)
	}
	return def, nil
}

// CompleteInput asks the LLM for the agent's next turn.
type CompleteInput struct {
	Agent    string        `json:"agent"`
	Messages []llm.Message `json:"messages"`
}

func (a *Activities) Complete(ctx context.Context, in CompleteInput) (*llm.CompletionResponse, error) {
	def, err := a.agent(in.Agent)
	if err != nil {
		return nil, err
	}
	return a.LLM.Complete(ctx, llm.CompletionRequest{
		Model:     def.Model,
		System:    def.System,
		Messages:  in.Messages,
		Tools:     def.toolDefs(),
		MaxTokens: def.MaxTokens,
	})
}

// ProposeActionInput records one proposed tool call and runs it through the
// policy. Idempotent on (RunID, ToolCall.ID).
type ProposeActionInput struct {
	RunID string       `json:"run_id"`
	OrgID string       `json:"org_id"`
	Agent string       `json:"agent"`
	Call  llm.ToolCall `json:"call"`
}

// ProposeAction creates the Action record, appends action_proposed, and
// evaluates the policy. Auto-approvals and policy-forbids are recorded as
// decisions immediately; everything else lands in pending_approval for a
// human. Returns the action in its post-policy state.
func (a *Activities) ProposeAction(ctx context.Context, in ProposeActionInput) (*agentkit.Action, error) {
	def, err := a.agent(in.Agent)
	if err != nil {
		return nil, err
	}

	action := agentkit.Action{
		ID:         agentkit.NewID(),
		OrgID:      in.OrgID,
		RunID:      in.RunID,
		ToolCallID: in.Call.ID,
		Tool:       in.Call.Name,
		Input:      in.Call.Input,
		State:      agentkit.ActionPendingApproval,
	}
	stored, err := a.Store.ProposeAction(ctx, action, event(action.OrgID, action.RunID,
		agentkit.EventActionProposed, "action_proposed:"+in.Call.ID,
		map[string]any{"action_id": action.ID, "tool": action.Tool}))
	if err != nil {
		return nil, err
	}
	if stored.Decision != nil {
		return stored, nil // retried proposal that was already decided
	}

	switch def.Policy.Evaluate(ctx, *stored) {
	case agentkit.AutoApprove:
		return a.recordDecision(ctx, stored.ID, agentkit.Decision{
			Kind:      agentkit.DecisionApprove,
			DecidedBy: agentkit.DecidedByPolicy,
			Reason:    "auto-approved by policy",
		}, nil, stored.OrgID, stored.RunID)
	case agentkit.Forbid:
		return a.recordDecision(ctx, stored.ID, agentkit.Decision{
			Kind:      agentkit.DecisionReject,
			DecidedBy: agentkit.DecidedByPolicy,
			Reason:    "forbidden by policy",
		}, nil, stored.OrgID, stored.RunID)
	default:
		return stored, nil // waits for a human decision signal
	}
}

// RecordDecisionInput applies a human decision delivered via signal.
type RecordDecisionInput struct {
	OrgID    string         `json:"org_id"`
	RunID    string         `json:"run_id"`
	Decision DecisionSignal `json:"decision"`
}

func (a *Activities) RecordDecision(ctx context.Context, in RecordDecisionInput) (*agentkit.Action, error) {
	d := agentkit.Decision{Kind: in.Decision.Kind, DecidedBy: in.Decision.DecidedBy, Reason: in.Decision.Reason}
	if d.DecidedBy == "" {
		d.DecidedBy = "dispatcher"
	}
	return a.recordDecision(ctx, in.Decision.ActionID, d, in.Decision.EditedInput, in.OrgID, in.RunID)
}

func (a *Activities) recordDecision(ctx context.Context, actionID string, d agentkit.Decision, editedInput json.RawMessage, orgID, runID string) (*agentkit.Action, error) {
	return a.Store.RecordDecision(ctx, actionID, d, editedInput, event(orgID, runID,
		agentkit.EventDecisionMade, "decision_made:"+actionID,
		map[string]any{"action_id": actionID, "kind": d.Kind, "decided_by": d.DecidedBy, "reason": d.Reason}))
}

// ExecuteActionInput executes an approved action's tool.
type ExecuteActionInput struct {
	ActionID string `json:"action_id"`
	Agent    string `json:"agent"`
}

// ExecuteActionResult carries the finished action plus whether its tool is
// terminal for the agent — computed here so the workflow's terminality check
// is plain activity output, safe under replay.
type ExecuteActionResult struct {
	Action   *agentkit.Action `json:"action"`
	Terminal bool             `json:"terminal"`
}

// ExecuteAction runs the tool with the action's effective input (the human
// edit when present, else the agent's proposal) and records the result. If
// the action already finished — e.g. the activity is being retried — the
// stored outcome is returned without re-executing.
func (a *Activities) ExecuteAction(ctx context.Context, in ExecuteActionInput) (*ExecuteActionResult, error) {
	def, err := a.agent(in.Agent)
	if err != nil {
		return nil, err
	}
	action, err := a.Store.GetAction(ctx, in.ActionID)
	if err != nil {
		return nil, err
	}
	if action.State == agentkit.ActionCompleted || action.State == agentkit.ActionFailed {
		return a.executeResult(def, action), nil
	}
	if action.State != agentkit.ActionApproved && action.State != agentkit.ActionApprovedWithEdits {
		return nil, fmt.Errorf("temporalkit: action %s not approved (state %s)", action.ID, action.State)
	}
	tool, ok := def.Tools[action.Tool]
	if !ok {
		return nil, fmt.Errorf("temporalkit: agent %s has no tool %q", in.Agent, action.Tool)
	}

	// The one place a tool ever executes: inside the action pipeline.
	execCtx := agentkit.WithRunContext(ctx, agentkit.RunContext{RunID: action.RunID, OrgID: action.OrgID})
	result, execErr := tool.Execute(execCtx, action.EffectiveInput())

	eventType := agentkit.EventActionExecuted
	execErrMsg := ""
	if execErr != nil {
		eventType = agentkit.EventActionFailed
		execErrMsg = execErr.Error()
	}
	finished, err := a.Store.FinishAction(ctx, action.ID, result, execErrMsg, event(action.OrgID, action.RunID,
		eventType, "action_executed:"+action.ID,
		map[string]any{"action_id": action.ID, "tool": action.Tool, "error": execErrMsg}))
	if err != nil {
		return nil, err
	}
	return a.executeResult(def, finished), nil
}

func (a *Activities) executeResult(def AgentDefinition, action *agentkit.Action) *ExecuteActionResult {
	return &ExecuteActionResult{
		Action:   action,
		Terminal: action.State == agentkit.ActionCompleted && def.isTerminal(action.Tool),
	}
}

// FinishRunInput marks the run's terminal status.
type FinishRunInput struct {
	RunID  string             `json:"run_id"`
	OrgID  string             `json:"org_id"`
	Status agentkit.RunStatus `json:"status"`
}

func (a *Activities) FinishRun(ctx context.Context, in FinishRunInput) error {
	eventType := agentkit.EventRunCompleted
	if in.Status == agentkit.RunFailed {
		eventType = agentkit.EventRunFailed
	}
	return a.Store.FinishRun(ctx, in.RunID, in.Status, event(in.OrgID, in.RunID,
		eventType, string(eventType)+":"+in.RunID, map[string]any{"status": in.Status}))
}

func event(orgID, runID string, t agentkit.EventType, dedupeKey string, payload map[string]any) agentkit.Event {
	raw, _ := json.Marshal(payload)
	return agentkit.Event{
		ID:        agentkit.NewID(),
		OrgID:     orgID,
		RunID:     runID,
		Type:      t,
		Payload:   raw,
		DedupeKey: dedupeKey,
	}
}
