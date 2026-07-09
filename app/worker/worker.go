// Package worker assembles the Temporal worker: agent definitions, the
// agent-loop workflow, and activities with their dependencies.
package worker

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"dispatch/agentkit/llm"
	"dispatch/agentkit/store"
	"dispatch/agentkit/temporalkit"
	"dispatch/app"
	"dispatch/app/agents/intake"
	"dispatch/app/channel"
	"dispatch/app/channel/dev"
	"dispatch/app/domain"
)

// New builds the dispatch worker on the shared task queue.
func New(tc temporalclient.Client, pool *pgxpool.Pool, model string, llmClient llm.LLM) worker.Worker {
	appStore := domain.NewStore(pool)
	sender := channel.NewSender(appStore, channel.NewRegistry(dev.New(appStore)))
	def := intake.Definition(model, appStore, sender)

	acts := &temporalkit.Activities{
		LLM:   llmClient,
		Store: store.NewPostgres(pool),
		Agents: map[string]temporalkit.AgentDefinition{
			def.Name: def,
		},
		// A tripped turn budget means the agent was acting without a human in
		// the path; flag the conversation so a dispatcher engages (the same
		// attention projection the escalate tool uses).
		TurnBudgetExceeded: func(ctx context.Context, runID, orgID string) error {
			conv, err := appStore.GetConversationByRunID(ctx, runID)
			if err != nil {
				return err
			}
			return appStore.RaiseEscalation(ctx, conv.ID,
				"Agent paused: it hit the per-turn LLM call budget. Review the conversation and reply to resume.")
		},
	}

	w := worker.New(tc, app.TaskQueue, worker.Options{})
	temporalkit.Register(w, acts)
	return w
}
