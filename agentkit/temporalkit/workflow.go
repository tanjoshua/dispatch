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
	dispatcherCh := workflow.GetSignalChannel(ctx, SignalDispatcherMessage)

	for {
		// Durable wait: block until the customer or the dispatcher says
		// something, then absorb any backlog. Both land in the shared context;
		// the agent runs a turn only when the *customer* spoke — a dispatcher
		// message informs the agent without provoking it to talk over a human
		// who is handling the conversation (design/003-dispatcher-as-participant.md).
		sawCustomer := awaitContext(ctx, inboundCh, dispatcherCh, &messages)
		if sawCustomer {
			terminal, err := agentTurn(llmCtx, actCtx, decisionCh, dispatcherCh, input, &messages)
			if err != nil {
				finishRun(actCtx, input, agentkit.RunFailed, logger)
				return err
			}
			if terminal {
				return finishRun(actCtx, input, agentkit.RunCompleted, logger)
			}
		}

		// Between turns is the safe point to roll over a long history.
		// Conversation context is carried in the new input.
		if workflow.GetInfo(ctx).GetContinueAsNewSuggested() && inboundCh.Len() == 0 && dispatcherCh.Len() == 0 {
			next := input
			next.Messages = messages
			return workflow.NewContinueAsNewError(ctx, AgentLoopWorkflowName, next)
		}
	}
}

// awaitContext blocks until at least one inbound or dispatcher message is
// available, then drains all buffered messages of both kinds into the shared
// context. It reports whether any *customer* (inbound) message arrived — the
// trigger for an agent turn. A dispatcher message updates context without
// provoking a turn.
func awaitContext(ctx workflow.Context, inboundCh, dispatcherCh workflow.ReceiveChannel, messages *[]llm.Message) bool {
	sawCustomer := false
	sel := workflow.NewSelector(ctx)
	sel.AddReceive(inboundCh, func(c workflow.ReceiveChannel, _ bool) {
		var m InboundMessage
		c.Receive(ctx, &m)
		*messages = append(*messages, llm.UserText(m.Text))
		sawCustomer = true
	})
	sel.AddReceive(dispatcherCh, func(c workflow.ReceiveChannel, _ bool) {
		var m DispatcherMessageSignal
		c.Receive(ctx, &m)
		*messages = append(*messages, DispatcherContextMessage(m.Text))
	})
	sel.Select(ctx) // block for the first
	// Drain the rest without blocking, checking both kinds each pass so a
	// backlog stays roughly in arrival order.
	for {
		var in InboundMessage
		if inboundCh.ReceiveAsync(&in) {
			*messages = append(*messages, llm.UserText(in.Text))
			sawCustomer = true
			continue
		}
		var dm DispatcherMessageSignal
		if dispatcherCh.ReceiveAsync(&dm) {
			*messages = append(*messages, DispatcherContextMessage(dm.Text))
			continue
		}
		break
	}
	return sawCustomer
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
func agentTurn(llmCtx, actCtx workflow.Context, decisionCh, dispatcherCh workflow.ReceiveChannel, input AgentLoopInput, messages *[]llm.Message) (terminal bool, err error) {
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
		var notes []string // dispatcher messages that arrived mid-turn
		standDown := false // a draft was dismissed or superseded: end the turn
		for _, call := range calls {
			outcome, note, err := decideAndExecute(actCtx, decisionCh, dispatcherCh, input, call)
			if err != nil {
				return false, err
			}
			results = append(results, feedback(outcome.Action, call))
			if outcome.Terminal {
				terminal = true
			}
			if isStandDown(outcome.Action) {
				standDown = true
			}
			if note != "" {
				notes = append(notes, note)
			}
		}
		// All results for one assistant turn go back in a single user message;
		// any dispatcher messages that arrived during the turn ride along in
		// that same turn, keeping conversation roles alternating.
		*messages = append(*messages, toolResultsWithNotes(results, notes))
		if terminal {
			return true, nil
		}
		if standDown {
			// The dispatcher dismissed a draft, or answered the customer
			// directly. The agent does not re-draft now; it waits for the
			// customer's next message, which re-engages it with full context
			// (the dismissal/dispatcher reply is already in context above).
			return false, nil
		}
		// Loop: the agent sees decisions/results and may revise (e.g. after
		// a rejection) or propose further actions.
	}
}

// isStandDown reports whether an action was resolved in a way that ends the
// agent's turn without re-drafting: a dispatcher dismissal (escape) or a
// supersede (the dispatcher answered the customer directly).
func isStandDown(action *agentkit.Action) bool {
	if action == nil || action.Decision == nil {
		return false
	}
	return action.Decision.Kind == agentkit.DecisionDismiss ||
		action.Decision.Kind == agentkit.DecisionSupersede
}

// toolResultsWithNotes packs one assistant turn's tool results, plus any
// dispatcher messages that arrived during the turn, into a single user turn.
func toolResultsWithNotes(results []llm.ToolResult, notes []string) llm.Message {
	m := llm.ToolResults(results...)
	for _, n := range notes {
		m.Content = append(m.Content, llm.ContentBlock{Type: "text", Text: n})
	}
	return m
}

// decideAndExecute takes one tool call through the full action pipeline. While a
// proposed action waits for a human decision, a dispatcher message may arrive
// instead — the human answered the customer directly — which supersedes the
// pending action and returns the dispatcher's text as a context note.
func decideAndExecute(actCtx workflow.Context, decisionCh, dispatcherCh workflow.ReceiveChannel, input AgentLoopInput, call llm.ToolCall) (*ExecuteActionResult, string, error) {
	logger := workflow.GetLogger(actCtx)

	var action agentkit.Action
	err := workflow.ExecuteActivity(actCtx, "ProposeAction", ProposeActionInput{
		RunID: input.RunID,
		OrgID: input.OrgID,
		Agent: input.Agent,
		Call:  call,
	}).Get(actCtx, &action)
	if err != nil {
		return nil, "", fmt.Errorf("propose action: %w", err)
	}

	// HITL is policy, not architecture: the wait only happens when the policy
	// said RequireApproval. Durable — a decision can take days. The dispatcher
	// can also message the customer directly at any time, which supersedes this
	// draft rather than deciding on it (design/003-dispatcher-as-participant.md).
	note := ""
	for action.State == agentkit.ActionPendingApproval {
		var decision *DecisionSignal
		var disp *DispatcherMessageSignal
		sel := workflow.NewSelector(actCtx)
		sel.AddReceive(decisionCh, func(c workflow.ReceiveChannel, _ bool) {
			var d DecisionSignal
			c.Receive(actCtx, &d)
			decision = &d
		})
		sel.AddReceive(dispatcherCh, func(c workflow.ReceiveChannel, _ bool) {
			var m DispatcherMessageSignal
			c.Receive(actCtx, &m)
			disp = &m
		})
		sel.Select(actCtx)

		if disp != nil {
			// The dispatcher answered directly. Withdraw this draft as
			// superseded and carry their message into context. The message is
			// already delivered + persisted by the reply endpoint; here we only
			// resolve the action and note the text for the agent.
			err := workflow.ExecuteActivity(actCtx, "RecordDecision", RecordDecisionInput{
				OrgID: input.OrgID,
				RunID: input.RunID,
				Decision: DecisionSignal{
					ActionID:  action.ID,
					Kind:      agentkit.DecisionSupersede,
					DecidedBy: "dispatcher",
					Reason:    "dispatcher replied to the customer directly",
				},
			}).Get(actCtx, &action)
			if err != nil {
				return nil, "", fmt.Errorf("record decision: %w", err)
			}
			note = DispatcherNote(disp.Text)
			continue
		}

		if decision.ActionID != action.ID {
			logger.Warn("decision for unexpected action ignored", "got", decision.ActionID, "want", action.ID)
			continue
		}
		err := workflow.ExecuteActivity(actCtx, "RecordDecision", RecordDecisionInput{
			OrgID:    input.OrgID,
			RunID:    input.RunID,
			Decision: *decision,
		}).Get(actCtx, &action)
		if err != nil {
			return nil, "", fmt.Errorf("record decision: %w", err)
		}
	}

	if action.State == agentkit.ActionApproved || action.State == agentkit.ActionApprovedWithEdits {
		var result ExecuteActionResult
		err := workflow.ExecuteActivity(actCtx, "ExecuteAction", ExecuteActionInput{
			ActionID: action.ID,
			Agent:    input.Agent,
		}).Get(actCtx, &result)
		if err != nil {
			return nil, "", fmt.Errorf("execute action: %w", err)
		}
		return &result, note, nil
	}
	return &ExecuteActionResult{Action: &action}, note, nil
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
// dispatcher dismisses (escapes) a draft: it was not sent, and the agent is not
// asked to revise now. It waits for the customer's next message before drafting
// again — the same as after any completed turn (this is "not this draft", not a
// control handoff; see design/003). It must NOT be recognized as rejection
// feedback, or a re-drafting loop would treat the escape as a revise.
func DismissFeedback() string {
	return "The dispatcher chose not to send this draft. " +
		"Do not re-send it now; wait for the customer's next message before drafting again."
}

// SupersedeFeedback renders the tool-result content the agent sees when a
// dispatcher answered the customer directly instead of ruling on this draft.
// The dispatcher's own message (delivered to the customer) is added to the
// context alongside this result via DispatcherNote, so the agent knows what was
// said. Like a dismiss, the agent stands down for this turn.
func SupersedeFeedback() string {
	return "The dispatcher replied to the customer directly instead of sending this draft, " +
		"so it was not sent. Their message is shown below. " +
		"Do not repeat it; wait for the customer's next message before drafting again."
}

// DispatcherNote renders a message the dispatcher sent directly to the customer
// as an agent-facing context note, clearly attributed to the human operator so
// the agent never mistakes it for its own words or for the customer's.
func DispatcherNote(text string) string {
	return "[The human dispatcher sent this message to the customer directly]\n" + text
}

// DispatcherContextMessage wraps a dispatcher's direct message as a
// conversation turn for the agent's context (a labeled user turn — it is
// information the agent receives, not something it authored).
func DispatcherContextMessage(text string) llm.Message {
	return llm.UserText(DispatcherNote(text))
}

// feedback renders an action's outcome as the tool result the agent sees —
// including rejections (with reason), dismissals, supersedes, and human edits,
// so the agent revises, stands down, or proceeds rather than blindly repeating.
func feedback(action *agentkit.Action, call llm.ToolCall) llm.ToolResult {
	switch action.State {
	case agentkit.ActionRejected:
		if action.Decision != nil && action.Decision.Kind == agentkit.DecisionDismiss {
			return llm.ToolResult{ToolCallID: call.ID, Content: DismissFeedback()}
		}
		if action.Decision != nil && action.Decision.Kind == agentkit.DecisionSupersede {
			return llm.ToolResult{ToolCallID: call.ID, Content: SupersedeFeedback()}
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
