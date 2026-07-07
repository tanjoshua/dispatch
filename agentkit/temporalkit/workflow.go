package temporalkit

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"dispatch/agentkit"
	"dispatch/agentkit/llm"
)

// AgentLoopWorkflowName is the registered workflow type name.
const AgentLoopWorkflowName = "AgentLoop"

// Register wires the agent loop workflow and its activities into a worker.
func Register(w worker.Worker, acts *Activities) {
	w.RegisterWorkflowWithOptions(AgentLoopWorkflow, workflow.RegisterOptions{Name: AgentLoopWorkflowName})
	w.RegisterActivity(acts)
}

// AgentLoopWorkflow is the durable agent loop:
//
//	for run is open:
//	    await inbound message (durable wait, days OK)
//	    loop: LLM turn → for each proposed tool call:
//	        propose action → policy → (await human decision) → execute
//	        feed decision + result back into agent context
//	    until the agent stops proposing actions, or a terminal tool ran
//
// Workflow code only orchestrates; every side effect is an activity.
func AgentLoopWorkflow(ctx workflow.Context, input AgentLoopInput) error {
	logger := workflow.GetLogger(ctx)

	llmCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute, // LLM turns can run long
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: 2 * time.Second, MaximumAttempts: 5},
	})
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 5},
	})

	messages := input.Messages
	inboundCh := workflow.GetSignalChannel(ctx, SignalInboundMessage)
	decisionCh := workflow.GetSignalChannel(ctx, SignalDecision)

	for {
		// Durable wait for the next customer message; drain any backlog.
		var msg InboundMessage
		inboundCh.Receive(ctx, &msg)
		messages = append(messages, llm.UserText(msg.Text))
		for inboundCh.ReceiveAsync(&msg) {
			messages = append(messages, llm.UserText(msg.Text))
		}

		terminal, err := agentTurn(llmCtx, actCtx, decisionCh, input, &messages)
		if err != nil {
			finishRun(actCtx, input, agentkit.RunFailed, logger)
			return err
		}
		if terminal {
			return finishRun(actCtx, input, agentkit.RunCompleted, logger)
		}

		// Between customer turns is the safe point to roll over a long
		// history. Conversation context is carried in the new input.
		if workflow.GetInfo(ctx).GetContinueAsNewSuggested() && inboundCh.Len() == 0 {
			next := input
			next.Messages = messages
			return workflow.NewContinueAsNewError(ctx, AgentLoopWorkflowName, next)
		}
	}
}

func finishRun(actCtx workflow.Context, input AgentLoopInput, status agentkit.RunStatus, logger log.Logger) error {
	err := workflow.ExecuteActivity(actCtx, "FinishRun", FinishRunInput{
		RunID:  input.RunID,
		OrgID:  input.OrgID,
		Status: status,
	}).Get(actCtx, nil)
	if err != nil {
		logger.Error("finish run failed", "run_id", input.RunID, "error", err)
	}
	return err
}

// agentTurn runs LLM completions until the agent stops proposing tool calls
// or a terminal tool executes successfully.
func agentTurn(llmCtx, actCtx workflow.Context, decisionCh workflow.ReceiveChannel, input AgentLoopInput, messages *[]llm.Message) (terminal bool, err error) {
	logger := workflow.GetLogger(llmCtx)

	for {
		var resp llm.CompletionResponse
		err := workflow.ExecuteActivity(llmCtx, "Complete", CompleteInput{
			Agent:    input.Agent,
			Messages: *messages,
		}).Get(llmCtx, &resp)
		if err != nil {
			return false, fmt.Errorf("llm completion: %w", err)
		}
		if len(resp.Content) == 0 {
			logger.Warn("empty completion", "stop_reason", resp.StopReason)
			return false, nil
		}
		*messages = append(*messages, llm.AssistantMessage(&resp))

		calls := resp.ToolCalls()
		if len(calls) == 0 {
			return false, nil // agent yielded; wait for the next inbound message
		}

		var results []llm.ToolResult
		for _, call := range calls {
			outcome, err := decideAndExecute(actCtx, decisionCh, input, call)
			if err != nil {
				return false, err
			}
			results = append(results, feedback(outcome.Action, call))
			if outcome.Terminal {
				terminal = true
			}
		}
		// All results for one assistant turn go back in a single message.
		*messages = append(*messages, llm.ToolResults(results...))
		if terminal {
			return true, nil
		}
		// Loop: the agent sees decisions/results and may revise (e.g. after
		// a rejection) or propose further actions.
	}
}

// decideAndExecute takes one tool call through the full action pipeline.
func decideAndExecute(actCtx workflow.Context, decisionCh workflow.ReceiveChannel, input AgentLoopInput, call llm.ToolCall) (*ExecuteActionResult, error) {
	logger := workflow.GetLogger(actCtx)

	var action agentkit.Action
	err := workflow.ExecuteActivity(actCtx, "ProposeAction", ProposeActionInput{
		RunID: input.RunID,
		OrgID: input.OrgID,
		Agent: input.Agent,
		Call:  call,
	}).Get(actCtx, &action)
	if err != nil {
		return nil, fmt.Errorf("propose action: %w", err)
	}

	// HITL is policy, not architecture: the wait only happens when the
	// policy said RequireApproval. Durable — a decision can take days.
	for action.State == agentkit.ActionPendingApproval {
		var decision DecisionSignal
		decisionCh.Receive(actCtx, &decision)
		if decision.ActionID != action.ID {
			logger.Warn("decision for unexpected action ignored", "got", decision.ActionID, "want", action.ID)
			continue
		}
		err := workflow.ExecuteActivity(actCtx, "RecordDecision", RecordDecisionInput{
			OrgID:    input.OrgID,
			RunID:    input.RunID,
			Decision: decision,
		}).Get(actCtx, &action)
		if err != nil {
			return nil, fmt.Errorf("record decision: %w", err)
		}
	}

	if action.State == agentkit.ActionApproved || action.State == agentkit.ActionApprovedWithEdits {
		var result ExecuteActionResult
		err := workflow.ExecuteActivity(actCtx, "ExecuteAction", ExecuteActionInput{
			ActionID: action.ID,
			Agent:    input.Agent,
		}).Get(actCtx, &result)
		if err != nil {
			return nil, fmt.Errorf("execute action: %w", err)
		}
		return &result, nil
	}
	return &ExecuteActionResult{Action: &action}, nil
}

// feedback renders an action's outcome as the tool result the agent sees —
// including rejections (with reason) and human edits, so the agent revises
// rather than repeats.
func feedback(action *agentkit.Action, call llm.ToolCall) llm.ToolResult {
	switch action.State {
	case agentkit.ActionRejected:
		reason := action.Decision.Reason
		if reason == "" {
			reason = "no reason given"
		}
		return llm.ToolResult{
			ToolCallID: call.ID,
			Content: "The dispatcher REJECTED this action. Reason: " + reason +
				"\nRevise your approach based on this feedback; do not repeat the same proposal.",
		}
	case agentkit.ActionFailed:
		return llm.ToolResult{ToolCallID: call.ID, Content: "Tool execution failed: " + action.Error, IsError: true}
	case agentkit.ActionCompleted:
		content := string(action.Result)
		if content == "" {
			content = "ok"
		}
		if len(action.EditedInput) > 0 {
			content += "\n\nNote: the dispatcher edited your proposed input before execution. Executed input: " + string(action.EditedInput)
		}
		return llm.ToolResult{ToolCallID: call.ID, Content: content}
	default:
		return llm.ToolResult{ToolCallID: call.ID, Content: "Action ended in unexpected state: " + string(action.State), IsError: true}
	}
}
