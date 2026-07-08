# Implementation Status

Tracks what is built against `docs/design/000-foundation.md`. Update this
when a milestone lands or scope changes.

_Last updated: 2026-07-05 — v1 WhatsApp intake demo implemented and verified
end-to-end._

## Completed (v1 intake demo)

### Infrastructure

- [x] Single Go module; repo layout per design doc §9
- [x] `docker-compose.yml`: Postgres 16 (:5432) + Temporal dev server
      (:7233, web UI :8233)
- [x] `migrations/0001_init.sql` + `cmd/migrate` runner
      (filename order, tracked in `schema_migrations`)
- [x] ULIDs everywhere; `org_id` on every table, parented by a real
      `organizations` row as of `design/002` (seeded `org_dev`)

### agentkit (business-agnostic layer)

- [x] Action lifecycle & state machine (`proposed → pending_approval →
      approved/approved_with_edits/rejected → executing → completed/failed`)
- [x] Original proposal preserved; edits stored in `edited_input`;
      rejections require a reason
- [x] Tool interface + ToolSet; `RunContext` injected via ctx so tools stay
      ignorant of Temporal
- [x] Policy interface + `StaticPolicy` (per-tool table,
      RequireApproval default; AutoApprove/Forbid supported)
- [x] Run tracking; append-only `events` log with idempotent
      (run_id, dedupe_key) appends
- [x] Postgres store; all mutations idempotent under activity retries
      (proposals dedupe on tool-call ID)
- [x] LLM abstraction (`agentkit/llm`): minimal chat-with-tools intersection,
      JSON-serializable across activity boundaries
- [x] Anthropic adapter on official `anthropic-sdk-go`
      (default `claude-opus-4-8`)
- [x] `temporalkit` agent loop: durable waits on `inbound_message` /
      `decision` signals, all side effects in activities, decision + result
      feedback into agent context, terminal tools end the run,
      ContinueAsNew between customer turns

### app (dispatch product)

- [x] Domain: customers, conversations, messages, jobs + Postgres store
- [x] Intake agent: system prompt; `send_message` / `update_job` /
      `close_job` / `escalate` tools; policy auto-approves `update_job` and
      `escalate`, requires approval for `send_message` and `close_job`
- [x] Escalation (`design/001-escalation.md`): `escalate` tool +
      conversation attention projection (migration `0002`) + acknowledge
      endpoint/event; dispatcher UI flags to top with safety-orange +
      Acknowledge. Agent escalates from judgment (no keyword rules); keyless
      demo LLM does not simulate it — exercised by the real agent
- [x] Dispatcher as participant (`design/003-dispatcher-as-participant.md`):
      the dispatcher can reply to the customer directly at any time
      (`POST /api/conversations/{id}/reply`) — no "agent's turn" / takeover.
      Messages carry an `author` (customer | agent | dispatcher, migration
      `0004`); a dispatcher reply goes out the shared `Sender` path, is recorded
      as a `dispatcher_message` event, and is signaled into the run's context so
      the agent's next turn is fully informed. A pending draft is `supersede`d
      when the dispatcher answers directly. `Dismiss` reframed to "discard this
      draft" (no takeover). UI: a dispatcher reply composer at the foot of the
      thread; dispatcher-authored bubbles stamped distinctly. Agent turns stay
      customer-driven; a dispatcher message is context, not a trigger
- [x] Org & channel connections (`design/002-organization-and-channels.md`):
      `organizations` + `channel_connections` tables (migration `0003`,
      seeds `org_dev` + `chan_dev`); channel split into per-kind `Adapter`
      + shared `Sender` (outbound) / `Router` (inbound); org resolved from
      the connection an inbound message arrives on, not a server global. Dev
      channel is the first connection kind, exercising the production path
- [x] JSON API: `POST /api/dev/inbound` (was `/api/simulate/inbound`, kept as
      a deprecated alias), `GET /api/conversations`,
      `GET /api/conversations/{id}`, `POST /api/actions/{id}/decision`,
      `GET /api/runs/{id}/events`
- [x] Temporal worker wiring; `cmd/server` + `cmd/worker` binaries
- [x] `DISPATCH_FAKE_LLM=1` scripted demo LLM (keyless demos and e2e tests)

### web (dispatcher UI)

- [x] Vite + React + TS, TanStack Router + Query, Tailwind v4;
      polling for liveness; `/api` proxied to the Go server
- [x] Conversation list with pending-review badges
- [x] Review timeline: WhatsApp thread interleaved with action tickets
- [x] Action ticket: Approve / Edit (JSON) / Reject (+reason), full audit of
      proposed vs edited vs decided
- [x] Job record panel (uncollected fields visible by design)
- [x] Customer simulator pane (per-conversation + new-conversation on `/`)

### Verified end-to-end (2026-07-05, scripted LLM, real Temporal + Postgres)

- [x] Inbound message → run started → actions proposed → pending approval
- [x] Approve → tool executes → job updated / reply sent
- [x] Approve-with-edits → edited input executes; original preserved
- [x] Reject with reason → agent revises instead of repeating
- [x] `close_job` → run completed, conversation closed, job intake_complete
- [x] New message after close → fresh run on a new conversation
- [x] Worker restart mid-run → workflow resumed, pending action intact
- [x] Event log carries the full audit trail per action

## Not yet done

- [ ] Live call through the Anthropic adapter (no API key in the dev
      environment; adapter compiles against the current SDK but is unexercised)
- [ ] Automated tests (e2e was exercised manually via the API; no `_test.go`
      files yet) + CI, including the agentkit-must-not-import-app lint
- [ ] Server embedding built web assets (single-binary deploy)
- [ ] Everything in design doc §8 non-goals: real WhatsApp adapters
      (Meta/Twilio), auto-approval policy demo ("the slider"), scheduling /
      follow-up agents, authn/authz, multi-org, learned confidence
- [ ] Escalation follow-ups (`design/001-escalation.md` §6 Future):
      context-aware auto-approval of safety messages while escalated, external
      notification (push/SMS), agentkit attention primitive. (Human takeover is
      no longer planned — superseded by `design/003`: the dispatcher is always a
      participant, so there is no turn to take over.)
- [ ] Open questions in design doc §11 (run granularity, decision timeouts,
      concurrent/batched actions, learning from edits)
