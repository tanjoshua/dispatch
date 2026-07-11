package temporalkit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
	Agents AgentResolver

	// TurnBudgetExceeded, when non-nil, is invoked after a turn_budget_exceeded
	// event is recorded, so the application can summon a human (flag the
	// conversation, page someone). agentkit stays domain-blind — reacting to a
	// tripped safety rail is the app's business.
	TurnBudgetExceeded func(ctx context.Context, runID, orgID string) error
	// ActionContext supplies application-owned freshness dependencies without
	// making agentkit import the application domain.
	ActionContext func(ctx context.Context, runID string) (revision int64, dependencies json.RawMessage, err error)
	// ModelTurns persists the exact request before a provider call and its
	// response afterward. Implementations dedupe by (run, sequence), preventing
	// a retry after a provider response from issuing a second model call.
	ModelTurns ModelTurnRecorder
}

type ModelTurnRecord struct {
	CandidateID          string                `json:"candidate_id"`
	RunID                string                `json:"run_id"`
	OrgID                string                `json:"org_id"`
	Seq                  int                   `json:"seq"`
	Agent                string                `json:"agent"`
	Request              llm.CompletionRequest `json:"request"`
	Tags                 map[string]string     `json:"tags,omitempty"`
	ContextRevision      int64                 `json:"context_revision"`
	EventToSeq           int64                 `json:"event_to_seq"`
	TriggeringMessageIDs []string              `json:"triggering_message_ids,omitempty"`
	DependencyVersions   json.RawMessage       `json:"dependency_versions,omitempty"`
}

type PreparedModelTurn struct {
	ID                 string                  `json:"id"`
	Response           *llm.CompletionResponse `json:"response,omitempty"`
	ContextRevision    int64                   `json:"context_revision,omitempty"`
	EventToSeq         int64                   `json:"event_to_seq,omitempty"`
	DependencyVersions json.RawMessage         `json:"dependency_versions,omitempty"`
}

type ModelTurnRecorder interface {
	PrepareModelTurn(context.Context, ModelTurnRecord) (*PreparedModelTurn, error)
	// CompleteModelTurn stores the first response and returns that canonical
	// response. Overlapping retries may call a provider twice, but only one
	// answer can reach the transcript and resulting Actions.
	CompleteModelTurn(context.Context, string, *llm.CompletionResponse) (*llm.CompletionResponse, error)
}

func (a *Activities) agent(ctx context.Context, orgID, runID, name string) (AgentDefinition, error) {
	if a.Agents == nil {
		return AgentDefinition{}, fmt.Errorf("temporalkit: no agent resolver configured")
	}
	return a.Agents.Resolve(ctx, orgID, runID, name)
}

// CompleteInput asks the LLM for the agent's next turn. Seq is the 1-based
// index of this completion within the run (deterministic in the workflow,
// survives ContinueAsNew); it keys the usage event idempotently, so a retried
// activity never double-counts.
//
// The conversation itself lives in Postgres (run_messages), not in the
// activity input — embedding it grew Temporal history O(n²). BaseSeq is the
// persisted transcript length before this completion; Delta is the new
// context since the last one (inbound messages, tool results), which the
// activity appends at BaseSeq before assembling the full transcript.
type CompleteInput struct {
	RunID   string        `json:"run_id"`
	OrgID   string        `json:"org_id"`
	Agent   string        `json:"agent"`
	Seq     int           `json:"seq"`
	BaseSeq int           `json:"base_seq"`
	Delta   []llm.Message `json:"delta,omitempty"`
	// SystemContext is the run's briefing (AgentLoopInput.SystemContext),
	// appended after the agent definition's system prompt — definition first
	// so the most stable text leads (prompt caching).
	SystemContext string `json:"system_context,omitempty"`
	// TriggeringMessageIDs are the persisted external messages carried by the
	// delta. They are stored with the context snapshot for audit/provenance.
	TriggeringMessageIDs []string `json:"triggering_message_ids,omitempty"`
}

// CompleteResult carries the model's reply plus the transcript length after
// this completion (Delta, plus the assistant turn when one was recorded) —
// the workflow's BaseSeq for the next call.
type CompleteResult struct {
	Response           *llm.CompletionResponse `json:"response"`
	TranscriptLen      int                     `json:"transcript_len"`
	ModelTurnID        string                  `json:"model_turn_id,omitempty"`
	ContextRevision    int64                   `json:"context_revision,omitempty"`
	EventToSeq         int64                   `json:"event_to_seq,omitempty"`
	DependencyVersions json.RawMessage         `json:"dependency_versions,omitempty"`
}

func (a *Activities) Complete(ctx context.Context, in CompleteInput) (*CompleteResult, error) {
	if err := a.Store.AppendRunMessages(ctx, in.RunID, in.OrgID, in.BaseSeq, in.Delta); err != nil {
		return nil, err
	}
	assistantSeq := in.BaseSeq + len(in.Delta)
	// Resolve after the delta is durable. Application resolvers attach a prompt
	// and freshness snapshot produced by one consistent database read.
	def, err := a.agent(ctx, in.OrgID, in.RunID, in.Agent)
	if err != nil {
		return nil, err
	}

	messages, err := a.Store.ListRunMessages(ctx, in.RunID, assistantSeq)
	if err != nil {
		return nil, err
	}
	system := def.System
	if in.SystemContext != "" {
		system += "\n\n" + in.SystemContext
	}
	request := llm.CompletionRequest{
		Model:     def.Model,
		System:    system,
		Messages:  messages,
		Tools:     def.toolDefs(),
		MaxTokens: def.MaxTokens,
	}
	var contextRevision int64
	var eventToSeq int64
	var dependencies json.RawMessage
	if def.Snapshot != nil {
		contextRevision = def.Snapshot.ContextRevision
		eventToSeq = def.Snapshot.EventToSeq
		dependencies = def.Snapshot.DependencyVersions
	} else if a.ActionContext != nil {
		contextRevision, dependencies, err = a.ActionContext(ctx, in.RunID)
		if err != nil {
			return nil, err
		}
	}
	modelTurnID := ""
	var preparedResponse *llm.CompletionResponse
	if a.ModelTurns != nil {
		candidateID := agentkit.NewID()
		prepared, err := a.ModelTurns.PrepareModelTurn(ctx, ModelTurnRecord{
			CandidateID: candidateID, RunID: in.RunID, OrgID: in.OrgID, Seq: in.Seq,
			Agent: in.Agent, Request: request, Tags: def.Tags,
			ContextRevision: contextRevision, EventToSeq: eventToSeq,
			TriggeringMessageIDs: in.TriggeringMessageIDs, DependencyVersions: dependencies,
		})
		if err != nil {
			return nil, err
		}
		modelTurnID, preparedResponse = prepared.ID, prepared.Response
		contextRevision, eventToSeq, dependencies = prepared.ContextRevision, prepared.EventToSeq, prepared.DependencyVersions
	}

	// A retried activity whose first attempt already recorded the assistant
	// turn returns the stored turn. The prepared model turn still supplies the
	// immutable dependency snapshot for actions from that completion.
	if stored, ok, err := a.Store.GetRunMessage(ctx, in.RunID, assistantSeq); err != nil {
		return nil, err
	} else if ok {
		resp := preparedResponse
		if resp == nil {
			resp = &llm.CompletionResponse{Content: stored.Content, StopReason: llm.StopOther}
			if a.ModelTurns != nil {
				resp, err = a.ModelTurns.CompleteModelTurn(ctx, modelTurnID, resp)
				if err != nil {
					return nil, err
				}
			}
		}
		return &CompleteResult{Response: resp, TranscriptLen: assistantSeq + 1, ModelTurnID: modelTurnID,
			ContextRevision: contextRevision, EventToSeq: eventToSeq, DependencyVersions: dependencies}, nil
	}

	started := time.Now()
	resp := preparedResponse
	if resp == nil {
		resp, err = a.LLM.Complete(ctx, request)
		if err != nil {
			return nil, err
		}
		if a.ModelTurns != nil {
			resp, err = a.ModelTurns.CompleteModelTurn(ctx, modelTurnID, resp)
			if err != nil {
				return nil, err
			}
		}
	}
	// Usage lands in the event log as it happens — it is the billing/eval/cost
	// substrate and cannot be backfilled. It goes in before the assistant turn:
	// once the turn exists, retries take the stored-turn path above and would
	// never write the event.
	payload := map[string]any{
		"model": def.Model, "input_tokens": resp.Usage.InputTokens,
		"output_tokens": resp.Usage.OutputTokens, "stop_reason": resp.StopReason,
		"seq": in.Seq, "duration_ms": time.Since(started).Milliseconds(), "model_turn_id": modelTurnID,
	}
	for key, value := range def.Tags {
		payload[key] = value
	}
	if err := a.Store.AppendEvent(ctx, event(in.OrgID, in.RunID,
		agentkit.EventLLMCompleted, fmt.Sprintf("llm_completed:%d", in.Seq),
		payload)); err != nil {
		return nil, err
	}
	// An empty reply is not recorded (an empty assistant turn would poison
	// future requests); the transcript then ends at the delta.
	transcriptLen := assistantSeq
	if len(resp.Content) > 0 {
		if err := a.Store.AppendRunMessages(ctx, in.RunID, in.OrgID, assistantSeq,
			[]llm.Message{llm.AssistantMessage(resp)}); err != nil {
			return nil, err
		}
		transcriptLen = assistantSeq + 1
	}
	return &CompleteResult{Response: resp, TranscriptLen: transcriptLen, ModelTurnID: modelTurnID,
		ContextRevision: contextRevision, EventToSeq: eventToSeq, DependencyVersions: dependencies}, nil
}

// TurnBudgetExceededInput records that a turn hit MaxLLMCallsPerTurn. Seq is
// the run's completion counter at the stop (the idempotency key).
type TurnBudgetExceededInput struct {
	RunID string `json:"run_id"`
	OrgID string `json:"org_id"`
	Agent string `json:"agent"`
	Seq   int    `json:"seq"`
	Calls int    `json:"calls"`
}

// RecordTurnBudgetExceeded appends the safety-rail event and gives the
// application its hook to summon a human. The turn has already been stopped by
// the workflow; this only records and reacts.
func (a *Activities) RecordTurnBudgetExceeded(ctx context.Context, in TurnBudgetExceededInput) error {
	if err := a.Store.AppendEvent(ctx, event(in.OrgID, in.RunID,
		agentkit.EventTurnBudgetExceeded, fmt.Sprintf("turn_budget_exceeded:%d", in.Seq),
		map[string]any{"agent": in.Agent, "calls": in.Calls, "seq": in.Seq})); err != nil {
		return err
	}
	if a.TurnBudgetExceeded != nil {
		return a.TurnBudgetExceeded(ctx, in.RunID, in.OrgID)
	}
	return nil
}

// DroppedDecisionInput records a decision the workflow could not apply.
type DroppedDecisionInput struct {
	RunID      string         `json:"run_id"`
	OrgID      string         `json:"org_id"`
	Decision   DecisionSignal `json:"decision"`
	DropReason string         `json:"drop_reason"`
}

// RecordDroppedDecision appends the decision_dropped event. The dedupe key is
// the decision's content (action, kind, decider), so activity retries and a
// client re-sending the identical ruling collapse to one event while distinct
// rulings each get recorded.
func (a *Activities) RecordDroppedDecision(ctx context.Context, in DroppedDecisionInput) error {
	d := in.Decision
	return a.Store.AppendEvent(ctx, event(in.OrgID, in.RunID,
		agentkit.EventDecisionDropped,
		fmt.Sprintf("decision_dropped:%s:%s:%s", d.ActionID, d.Kind, d.DecidedBy),
		map[string]any{
			"action_id":   d.ActionID,
			"kind":        d.Kind,
			"decided_by":  d.DecidedBy,
			"reason":      d.Reason,
			"drop_reason": in.DropReason,
		}))
}

// ProposeActionInput records one proposed tool call and runs it through the
// policy. Idempotent on (RunID, ToolCall.ID).
type ProposeActionInput struct {
	RunID              string          `json:"run_id"`
	OrgID              string          `json:"org_id"`
	Agent              string          `json:"agent"`
	Call               llm.ToolCall    `json:"call"`
	ModelTurnID        string          `json:"model_turn_id,omitempty"`
	ContextRevision    int64           `json:"context_revision,omitempty"`
	EventToSeq         int64           `json:"event_to_seq,omitempty"`
	DependencyVersions json.RawMessage `json:"dependency_versions,omitempty"`
}

// ProposeAction creates the Action record, appends action_proposed, and
// evaluates the policy. Auto-approvals and policy-forbids are recorded as
// decisions immediately; everything else lands in pending_approval for a
// human. Returns the action in its post-policy state.
func (a *Activities) ProposeAction(ctx context.Context, in ProposeActionInput) (*agentkit.Action, error) {
	def, err := a.agent(ctx, in.OrgID, in.RunID, in.Agent)
	if err != nil {
		return nil, err
	}

	action := agentkit.Action{
		ID:          agentkit.NewID(),
		OrgID:       in.OrgID,
		RunID:       in.RunID,
		ToolCallID:  in.Call.ID,
		Tool:        in.Call.Name,
		Input:       in.Call.Input,
		State:       agentkit.ActionPendingApproval,
		ModelTurnID: in.ModelTurnID,
	}
	if in.ModelTurnID != "" {
		action.ContextRevision = in.ContextRevision
		action.DependencyVersions = in.DependencyVersions
	} else if a.ActionContext != nil {
		rev, deps, err := a.ActionContext(ctx, in.RunID)
		if err != nil {
			return nil, err
		}
		action.ContextRevision = rev
		action.DependencyVersions = deps
	}
	if in.Call.Name == "propose_response" {
		action.RespondsThroughEventSeq = in.EventToSeq
		if action.RespondsThroughEventSeq == 0 {
			// Compatibility for pre-versioned workflows whose model supplied the
			// cursor directly. New workflows always use the model-turn snapshot.
			var p struct {
				RespondsThroughEventSeq int64 `json:"responds_through_event_seq"`
			}
			_ = json.Unmarshal(in.Call.Input, &p)
			action.RespondsThroughEventSeq = p.RespondsThroughEventSeq
		}
	}
	stored, err := a.Store.ProposeAction(ctx, action, event(action.OrgID, action.RunID,
		agentkit.EventActionProposed, "action_proposed:"+in.Call.ID,
		map[string]any{"action_id": action.ID, "tool": action.Tool, "model_turn_id": action.ModelTurnID}))
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
	OrgID    string `json:"org_id"`
	Agent    string `json:"agent"`
}

// ExecuteActionResult carries the finished action plus whether its tool is
// terminal for the agent — computed here so the workflow's terminality check
// is plain activity output, safe under replay.
type ExecuteActionResult struct {
	Action   *agentkit.Action `json:"action"`
	Terminal bool             `json:"terminal"`
	Pause    bool             `json:"pause"`
}

// ExecuteAction runs the tool with the action's effective input (the human
// edit when present, else the agent's proposal) and records the result. If
// the action already finished — e.g. the activity is being retried — the
// stored outcome is returned without re-executing.
func (a *Activities) ExecuteAction(ctx context.Context, in ExecuteActionInput) (*ExecuteActionResult, error) {
	action, err := a.Store.GetAction(ctx, in.OrgID, in.ActionID)
	if err != nil {
		return nil, err
	}
	def, err := a.agent(ctx, action.OrgID, action.RunID, in.Agent)
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

	// Validate the effective input — the human edit when present, else the
	// agent's proposal — against the tool's schema before anything runs. This
	// is the one choke point every execution passes through; a failure is
	// recorded like any tool failure and fed back so the agent (or the next
	// human edit) can revise.
	var result json.RawMessage
	var execErr error
	if err := agentkit.ValidateToolInput(tool, action.EffectiveInput()); err != nil {
		execErr = err
	} else {
		// The one place a tool ever executes: inside the action pipeline.
		execCtx := agentkit.WithRunContext(ctx, agentkit.RunContext{
			RunID: action.RunID, OrgID: action.OrgID, ActionID: action.ID,
			ModelTurnID: action.ModelTurnID, ContextRevision: action.ContextRevision,
			EventToSeq: action.RespondsThroughEventSeq,
		})
		result, execErr = tool.Execute(execCtx, action.EffectiveInput())
	}

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
	terminal := action.State == agentkit.ActionCompleted && def.isTerminal(action.Tool)
	// Some output tools can finish only selected invocations (for example an
	// inquiry response, while an intake question keeps the run open). The tool
	// reports that durable outcome in its result; the activity interprets it so
	// workflow code stays domain-agnostic and deterministic.
	if action.State == agentkit.ActionCompleted {
		var result struct {
			CompleteRun bool `json:"complete_run"`
		}
		if json.Unmarshal(action.Result, &result) == nil && result.CompleteRun {
			terminal = true
		}
	}
	return &ExecuteActionResult{
		Action:   action,
		Terminal: terminal,
		Pause:    action.State == agentkit.ActionCompleted && def.isPause(action.Tool),
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
