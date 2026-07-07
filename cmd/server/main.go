// Command server runs the dispatch JSON API.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	temporalclient "go.temporal.io/sdk/client"

	akstore "dispatch/agentkit/store"
	"dispatch/app"
	"dispatch/app/agents/intake"
	"dispatch/app/domain"
	"dispatch/app/server"
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
	port := env("PORT", "8080")

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

	srv := &server.Server{
		Domain:    domain.NewStore(pool),
		Agentkit:  akstore.NewPostgres(pool),
		Temporal:  tc,
		OrgID:     app.OrgID,
		TaskQueue: app.TaskQueue,
		AgentName: intake.AgentName,
	}

	log.Printf("api server listening on :%s", port)
	if err := http.ListenAndServe(":"+port, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
