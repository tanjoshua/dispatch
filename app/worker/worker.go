// Package worker assembles the Temporal worker: agent definitions, the
// agent-loop workflow, and activities with their dependencies.
package worker

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"dispatch/agentkit/llm"
	"dispatch/agentkit/store"
	"dispatch/agentkit/temporalkit"
	"dispatch/app"
	"dispatch/app/agents/intake"
	agentresolve "dispatch/app/agents/resolve"
	"dispatch/app/channel"
	"dispatch/app/channel/dev"
	"dispatch/app/domain"
	"dispatch/app/notify"
	"dispatch/app/packs"
)

// New builds the dispatch worker on the shared task queue. notifier may be
// nil — escalations then only flag the UI queue.
func New(tc temporalclient.Client, pool *pgxpool.Pool, llmClient llm.LLM, notifier notify.Notifier) worker.Worker {
	appStore := domain.NewStore(pool)
	sender := channel.NewSender(appStore, channel.NewRegistry(dev.New(appStore)))
	resolver := agentresolve.New(appStore, packs.Default(), intake.Tools(appStore, sender, notifier))

	acts := &temporalkit.Activities{
		LLM:           llmClient,
		Store:         store.NewPostgres(pool),
		Agents:        resolver,
		ActionContext: appStore.ActionContext,
		ModelTurns:    appStore,
		// A tripped turn budget means the agent was acting without a human in
		// the path; flag the conversation so a dispatcher engages (the same
		// attention projection + notification path the escalate tool uses).
		TurnBudgetExceeded: func(ctx context.Context, runID, orgID string) error {
			conv, err := appStore.GetConversationByRunID(ctx, runID)
			if err != nil {
				return err
			}
			const reason = "Agent paused: it hit the per-turn LLM call budget. Review the conversation and reply to resume."
			newlyFlagged, err := appStore.RaiseEscalation(ctx, conv.ID, reason)
			if err != nil {
				return err
			}
			if newlyFlagged && notifier != nil {
				e := notify.Escalation{
					OrgID:          conv.OrgID,
					ConversationID: conv.ID,
					Reason:         reason,
					Source:         "turn_budget",
				}
				if cust, err := appStore.GetCustomer(ctx, conv.CustomerID); err == nil {
					e.CustomerName = cust.Name
				}
				if err := notifier.Notify(ctx, e); err != nil {
					log.Printf("turn budget: notification failed for conversation %s: %v", conv.ID, err)
				}
			}
			return nil
		},
	}

	w := worker.New(tc, app.TaskQueue, worker.Options{})
	temporalkit.Register(w, acts)
	return w
}
