# Dispatch — AI-Native Dispatch Software

AI agents do dispatch work (starting with WhatsApp job intake); humans review
agent actions like a developer reviews a coding agent — approve / edit /
reject — with a policy layer that slides toward full autonomy over time.

**Read `docs/OVERVIEW.md` before making architectural changes.** It is the
single living doc: current implementation + the prioritized gap list. Update
it when the implementation or priorities change. (The old numbered design
docs it consolidated live in git history.)

## Stack

- Go (single module), Temporal for durable agent runs, Postgres for app state
  and event projections.
- Web UI: React SPA in `web/` — Vite, TanStack Router + TanStack Query,
  Tailwind — talking to the Go server's JSON API. The API is the contract.
- LLM: provider-agnostic interface in `agentkit/llm`; Anthropic is the first
  adapter.

## Architecture (two layers)

- `agentkit/` — business-agnostic agent primitives: Action lifecycle,
  approval Policy, Tool interface, Run tracking, append-only event log, LLM
  abstraction, Temporal agent-loop pattern (`agentkit/temporalkit`).
- `app/` — the dispatch product: domain (jobs, customers, conversations),
  the intake agent (prompt + tool implementations), channels (simulated
  WhatsApp now, real adapters later), HTTP JSON API, Temporal worker.
- `web/` — the React SPA. Binaries in `cmd/server` and `cmd/worker`; SQL in
  `migrations/`; `docker-compose.yml` runs Postgres + Temporal dev server.

## Hard rules (violating these breaks the product thesis)

1. **`agentkit` must never import `app`.** agentkit knows nothing about
   dispatch/WhatsApp/jobs. If agentkit needs domain knowledge, the app passes
   it in via an interface. (Test: "would a non-dispatch agent business need
   this?" → agentkit; otherwise app.)
2. **No side doors around the Action pipeline.** Every agent tool execution —
   including auto-approved ones — creates an Action record and flows
   proposed → decided → executed. Never call a tool's `Execute` outside this
   pipeline.
3. **Preserve the agent's original proposal.** Human edits go in
   `EditedInput`; never overwrite `Input`. Rejections require a reason. Both
   are fed back into the agent's context and kept as future training data.
4. **The `events` table is append-only.** Never UPDATE or DELETE events. UI
   state is a projection of events, written by idempotent activities keyed on
   event ID.
5. **Temporal workflow code is deterministic.** All LLM calls, DB access,
   HTTP, clocks, and ID generation happen in activities. Workflows only
   orchestrate. Signals carry IDs and small payloads, not blobs.
6. **HITL is policy, not architecture.** Never hardcode "ask the human" into
   a flow; route through `Policy.Evaluate`. Autonomy increases by changing
   policy, never by restructuring the loop.

## Conventions

- IDs are ULIDs. Every table carries `org_id` (multi-tenancy from day one),
  even though v1 has one org.
- Workflow IDs: `run-{runID}`. Signals: `inbound_message`, `decision`.
- Keep the LLM interface to the intersection of tool-calling chat APIs;
  provider-specific features live in adapter options, not the core interface.
- Channel adapters normalize inbound messages and signal the run's workflow;
  the agent and UI never know which channel is in use.

## Development

- `docker compose up -d` — Postgres (:5432) + Temporal dev server (:7233,
  web UI :8233).
- `go run ./cmd/migrate` — applies `migrations/*.sql` in filename order,
  tracked in `schema_migrations`.
- `go run ./cmd/worker` — Temporal worker. Needs `ANTHROPIC_API_KEY`;
  `DISPATCH_FAKE_LLM=1` swaps in a scripted demo LLM (no API calls).
  The model is resolved per playbook from its tier or advanced raw-ID override.
- `go run ./cmd/server` — JSON API on `:8080` (`PORT` to override).
- `cd web && npm install && npm run dev` — dispatcher UI on `:5173`;
  Vite proxies `/api` to `:8080`.
- `DATABASE_URL` / `TEMPORAL_ADDRESS` default to the docker-compose values.
- Checks: `go build ./... && go vet ./...`; web: `npm run build`
  (tsc + vite).
