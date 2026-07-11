// Package intake implements the field-service pack's tool set. Prompt,
// model, and policy configuration are resolved per organization and run.
package intake

import (
	"dispatch/agentkit"
	"dispatch/app/channel"
	"dispatch/app/domain"
	"dispatch/app/notify"
)

const AgentName = "intake"

func Tools(store *domain.Store, sender *channel.Sender, notifier notify.Notifier) agentkit.ToolSet {
	return agentkit.NewToolSet(
		&routeIntentTool{store: store},
		&listCandidateCasesTool{store: store},
		&selectCaseTool{store: store},
		&createCaseTool{store: store},
		&updateCaseTool{store: store},
		&proposeResponseTool{store: store, sender: sender},
		&escalateTool{store: store, notifier: notifier},
		&pauseTool{name: "stand_down"},
		&pauseTool{name: "wait_for_external"},
	)
}
