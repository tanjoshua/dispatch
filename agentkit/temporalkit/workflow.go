package temporalkit

import (
	"fmt"
	"strings"
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
		yield := false
		for _, call := range calls {
			outcome, err := decideAndExecute(actCtx, decisionCh, input, call)
			if err != nil {
				return false, err
			}
			results = append(results, feedback(outcome.Action, call))
			if outcome.Terminal {
				terminal = true
			}
			if isDismissed(outcome.Action) {
				yield = true
			}
		}
		// All results for one assistant turn go back in a single message.
		*messages = append(*messages, llm.ToolResults(results...))
		if terminal {
			return true, nil
		}
		if yield {
			// The dispatcher dismissed a draft: the agent stands down for now
			// rather than re-drafting. Stop the turn and wait for the next
			// inbound message, which re-engages the agent (the dismissal stays
			// in context, so it drafts fresh and aware). The tool results are
			// already appended above, keeping the history valid for that turn.
			return false, nil
		}
		// Loop: the agent sees decisions/results and may revise (e.g. after
		// a rejection) or propose further actions.
	}
}

// isDismissed reports whether an action was resolved by a dispatcher dismissal
// (escape) rather than an approval or a revise-style rejection.
func isDismissed(action *agentkit.Action) bool {
	return action != nil && action.Decision != nil &&
		action.Decision.Kind == agentkit.DecisionDismiss
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

// rejectionFeedbackPrefix opens the tool-result content the agent sees when a
// human rejects an action. It is a stable contract: anything that needs to
// recognize a rejection from the agent-facing message (a scripted/fake LLM, an
// eval) does so via IsRejectionFeedback rather than re-encoding the wording,
// so the producer and its recognizers can never drift apart.
const rejectionFeedbackPrefix = "The dispatcher REJECTED this action."

// RejectionFeedback renders the tool-result content the agent sees for a
// human-rejected action. Exported alongside IsRejectionFeedback so the two
// halves of the contract — producing the message and recognizing it — share
// one definition and can be exercised together in tests.
func RejectionFeedback(reason string) string {
	if reason == "" {
		reason = "no reason given"
	}
	return rejectionFeedbackPrefix + " Reason: " + reason +
		"\nRevise your approach based on this feedback; do not repeat the same proposal."
}

// IsRejectionFeedback reports whether a tool-result content string is the
// rejection feedback produced for a human-rejected action. It is the single
// source of truth for that recognition — the structured Action state is
// authoritative on the server, but the fake LLM only sees the rendered
// message, so it must be able to detect a rejection from the string alone.
func IsRejectionFeedback(content string) bool {
	return strings.HasPrefix(content, rejectionFeedbackPrefix)
}

// DismissFeedback renders the tool-result content the agent sees when a
// dispatcher dismisses (escapes) a draft. Unlike a rejection it does not ask
// for a revised proposal now: the agent stands down until the customer's next
// message. The workflow also hard-yields the turn, so this text mainly informs
// the agent on the next turn; it must NOT be recognized as rejection feedback,
// or a re-drafting loop would treat the escape as a revise.
func DismissFeedback() string {
	return "The dispatcher dismissed this draft and is handling the conversation for now. " +
		"Do not send another message; wait for the customer's next message before responding again."
}

// feedback renders an action's outcome as the tool result the agent sees —
// including rejections (with reason), dismissals, and human edits, so the agent
// revises, stands down, or proceeds rather than blindly repeating.
func feedback(action *agentkit.Action, call llm.ToolCall) llm.ToolResult {
	switch action.State {
	case agentkit.ActionRejected:
		if action.Decision != nil && action.Decision.Kind == agentkit.DecisionDismiss {
			return llm.ToolResult{ToolCallID: call.ID, Content: DismissFeedback()}
		}
		return llm.ToolResult{ToolCallID: call.ID, Content: RejectionFeedback(action.Decision.Reason)}
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
