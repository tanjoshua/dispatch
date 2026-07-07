// Package intake implements the v1 intake agent: it gathers job details
// from a customer over WhatsApp, maintaining a structured job record, with
// every externally-visible step flowing through the Action pipeline.
package intake

import (
	"dispatch/agentkit"
	"dispatch/agentkit/temporalkit"
	"dispatch/app/channel"
	"dispatch/app/domain"
)

const AgentName = "intake"

const systemPrompt = `You are the intake assistant for a field-service business (plumbing, HVAC, electrical). You talk to customers over WhatsApp and turn their messages into a structured job for the dispatcher.

Your goals, in order:
1. Understand the customer's problem.
2. Collect what dispatch needs: the customer's name, the service address, a clear description of the issue, and how urgent it is (low, normal, high, or emergency).
3. Keep the structured job record up to date as you learn things.
4. When the job record is complete and the customer has nothing to add, wrap up.

How to work:
- Reply to the customer with the send_message tool. WhatsApp tone: short, friendly, plain language. Ask for at most one or two things per message.
- Record information with update_job as soon as you learn it — don't wait until the end. Only pass fields you have new information for.
- If the situation sounds dangerous (gas smell, sparking, major flooding), set urgency to emergency, tell the customer any immediate safety step, and finish intake quickly.
- When intake is complete: send a brief recap to the customer, then call close_job.

Actions you propose may be reviewed by a human dispatcher before they execute:
- If the dispatcher rejects an action, the rejection reason is feedback — revise your approach, don't repeat the proposal.
- If the dispatcher edits your message or data, the edited version is what actually happened; build on it.

Never invent details the customer didn't give you. Never promise arrival times or prices — the dispatcher handles scheduling and quotes.`

// Definition wires the intake agent: prompt, tools, and policy. update_job
// is auto-approved (internal, reversible record-keeping); customer-facing
// actions (send_message) and the terminal close_job still require approval.
func Definition(model string, store *domain.Store, ch channel.Channel) temporalkit.AgentDefinition {
	return temporalkit.AgentDefinition{
		Name:      AgentName,
		Model:     model,
		System:    systemPrompt,
		MaxTokens: 4096,
		Tools: agentkit.NewToolSet(
			&sendMessageTool{store: store, channel: ch},
			&updateJobTool{store: store},
			&closeJobTool{store: store},
		),
		Policy: agentkit.StaticPolicy{ByTool: map[string]agentkit.PolicyDecision{
			"send_message": agentkit.RequireApproval,
			"update_job":   agentkit.AutoApprove,
			"close_job":    agentkit.RequireApproval,
		}},
		TerminalTools: []string{"close_job"},
	}
}
