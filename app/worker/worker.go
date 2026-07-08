// Package worker assembles the Temporal worker: agent definitions, the
// agent-loop workflow, and activities with their dependencies.
package worker

import (
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
	}

	w := worker.New(tc, app.TaskQueue, worker.Options{})
	temporalkit.Register(w, acts)
	return w
}
