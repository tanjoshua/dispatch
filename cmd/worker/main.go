// Command worker runs the Temporal worker hosting the agent loop.
package main

import (
	"context"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"dispatch/agentkit/llm"
	"dispatch/agentkit/llm/anthropic"
	"dispatch/app/agents/intake"
	"dispatch/app/notify"
	appworker "dispatch/app/worker"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	databaseURL := env("DATABASE_URL", "postgres://dispatch:dispatch@localhost:5432/dispatch?sslmode=disable")
	temporalAddr := env("TEMPORAL_ADDRESS", "localhost:7233")
	model := env("DISPATCH_MODEL", anthropic.DefaultModel)

	var llmClient llm.LLM = anthropic.New()
	if os.Getenv("DISPATCH_FAKE_LLM") != "" {
		llmClient = intake.ScriptedLLM{}
		log.Println("DISPATCH_FAKE_LLM set: using the scripted demo LLM, no API calls will be made")
	} else if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Println("warning: ANTHROPIC_API_KEY is not set; LLM calls will fail unless another credential source is configured (set DISPATCH_FAKE_LLM=1 for the scripted demo mode)")
	}

	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	tc, err := temporalclient.Dial(temporalclient.Options{HostPort: temporalAddr})
	if err != nil {
		log.Fatalf("connect temporal: %v", err)
	}
	defer tc.Close()

	var notifier notify.Notifier
	if url := os.Getenv("DISPATCH_ESCALATION_WEBHOOK_URL"); url != "" {
		notifier = notify.NewWebhook(url)
		log.Println("escalation notifications: webhook configured")
	} else {
		log.Println("warning: DISPATCH_ESCALATION_WEBHOOK_URL is not set; escalations only flag the UI queue (the escalate tool description reflects this)")
	}

	w := appworker.New(tc, pool, model, llmClient, notifier)
	log.Printf("worker starting (model=%s, temporal=%s)", model, temporalAddr)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalf("worker: %v", err)
	}
}
