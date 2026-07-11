# Dispatch — Implementation Overview & Roadmap

**Last updated:** 2026-07-10
**This is the single living doc:** what the product is, how it is built today,
and the gaps to tackle next. It consolidates and supersedes the numbered design
docs 000–005 and STATUS.md (all recoverable from git history; code comments
citing `design/00X §n` refer to those). Update this doc when the implementation
or the priorities change; a future feature that needs real design thought can
still get its own doc, folded back in here when built.

---

## 1. Product

AI-native dispatch software for field-service businesses (inspired by
probook.ai): **agents do the work — intake, scheduling, follow-up — and humans
review it** the way a developer reviews a coding agent, with a policy layer
that slides from review-everything toward full autonomy.

Three principles drive the architecture:

1. **Every externally visible agent act is a typed Action** a human can
   approve, edit, or reject before it executes.
2. **Human-in-the-loop is policy, not architecture.** v1: everything reviewed.
   v2: auto-approve low-risk tools. v3: exceptions only. Nothing structural
   changes between phases — only `Policy` does.
3. **The agent machinery (`agentkit`) is business-agnostic and reusable.**
   Dispatch is the first app built on it, not the only one.

**Verticals rule:** verticals are **code packs selected and parameterized by
config (playbooks)** — never a no-code workflow engine. Code owns the verbs
(tools, lifecycles, the loop); config owns which pack is active, with which
fields, under what policy, in whose voice.

## 2. Architecture

```
┌──────────────────────────────────────────────────────┐
│  web/       React SPA (Vite, TanStack Router/Query,  │
│             Tailwind) — talks only to the JSON API   │
├──────────────────────────────────────────────────────┤
│  app/       dispatch product: domain, intake agent,  │
│             channels (Sender/Router + adapters),     │
│             HTTP JSON API, Temporal worker           │
├──────────────────────────────────────────────────────┤
│  agentkit/  business-agnostic: Action lifecycle,     │
│             Policy, Tool, Run, append-only events,   │
│             LLM abstraction, temporalkit agent loop, │
│             Postgres store                           │
├──────────────────────────────────────────────────────┤
│  Temporal (durable runs) · Postgres (state +         │
│  projections)                                        │
└──────────────────────────────────────────────────────┘
```

The hard rules live in `CLAUDE.md` (agentkit never imports app; no side doors
around the Action pipeline; original proposals preserved; events append-only;
workflow determinism; HITL only via `Policy.Evaluate`). They are the product
thesis in rule form — violating them isn't a style problem.

**agentkit extraction trigger** (deliberate deferral): extract/publish agentkit
as its own module when a **second consumer** appears (primary signal — the
second consumer reveals the real public surface), or when the core primitives
stop churning *and* we want it public. Until then the boundary is held by the
import rule; a `go.work` in-repo module split is the reversible halfway step.

## 3. Domain model

```
Organization
 ├─ Playbook            selects the code agent (pack) + names the case type
 ├─ ChannelConnection   org's configured use of a channel kind; carries
 │                      default_playbook_id — where inbound routing binds
 ├─ Customer            CRM aggregate (person/business)
 │   └─ ContactIdentity (channel_kind, address) — one customer, many
 ├─ Conversation        ONE persistent thread per (contact identity × connection);
 │   └─ Message         never auto-closed; carries attention_state
 │                      (escalation projection) and author per message
 │                      (customer | agent | dispatcher)
 └─ Case                a unit of work; MANY per thread; typed core +
     └─ Run(s)          per-vertical JSONB data bag (playbook-owned schema).
                        A Run is ONE agent task (an intake), bound to
                        (conversation, case, playbook) via app-side
                        run_bindings; the case binds on first update_case.
```

Key cardinality decisions (these replaced the v1 1:1 chain):

- **Customer ≠ phone number.** Identity resolution is
  `(kind, address) → ContactIdentity → Customer`; same person across channels
  is one customer. Identity merge/dedup is a deferred follow-up.
- **Thread ≠ case lifecycle.** Threads persist forever; the *case* carries
  "is this work done". A thread is bound to the exact contact identity used on
  a channel connection; the customer view, not the thread, unifies identities
  and channels. Replies route through that same identity.
- **Run ≠ conversation.** Runs come and go per task; a thread has many, at
  most one awaiting customer input. `run_bindings` keeps agentkit's `runs`
  table business-agnostic. Its forward-only `stage` projection starts at
  triage, advances to inquiry after a case-less response, and advances to
  service job when a case is selected. The activity resolver reads it on each
  turn, so pack-owned stage models apply without workflow state or imports.
- **Playbook is the horizontal seam.** Connection → playbook → pack + case
  type. The field-service pack owns fixed lanes, tools, policy floors,
  per-stage models, and its prompt template. `playbooks.config` is a
  schema-versioned envelope (`pack`, voice, per-lane policy); models are
  vendor-owned, with a hidden per-stage override reserved for operations. It
  parameterizes those capabilities but cannot reorder them into a workflow.
- **Knowledge is organization-level.** `organizations.settings.profile`
  carries business name, hours, service area, and labeled facts. The pack
  prompt renders these as the only operational facts the agent may state.
  Organization, playbook, and channel-connection settings use versioned CAS
  writes. Every accepted command is snapshotted in append-only
  `config_revisions` with command ID and actor attribution.

## 4. The agent loop

One Temporal workflow per run (`run-{runID}`), signals `inbound_message`,
`decision`, `dispatcher_message`. This remains the active workflow while the
conversation-coordinator cutover described below is completed. All
non-determinism (LLM, DB, tool
execution, IDs) lives in activities; projections are idempotent via
`(run_id, dedupe_key)` on events and conditional updates on actions.

```
for run is open:
    await customer/dispatcher messages (durable, days OK)
      # deduped by message ID — redelivered signals never re-trigger a turn
    if a customer spoke:                 # dispatcher msgs inform, never trigger
        loop (≤ MaxLLMCallsPerTurn, exceed → record + app hook → human):
            LLM turn (Complete flushes the delta to the Postgres transcript,
                      assembles context there, records usage) →
            enforce one effectful tool call per completion
            for each permitted tool call:
              ProposeAction (with context + dependency versions) → Policy.Evaluate
                → auto-approve | Forbid | durable wait for decision signal
              approved → validate input vs schema → ExecuteAction
                         (the ONLY place a tool runs)
              feedback (result / rejection reason / edit note) → agent context
        until no more calls, terminal tool ran, or dismiss/supersede
    ContinueAsNew when history is large (input carries counters + the small
    unflushed delta; the transcript itself lives in run_messages)
```

- **Action lifecycle:** `proposed → pending_approval → approved /
  approved_with_edits / rejected / superseded → executing → completed /
  failed`. Decision
  kinds: `approve`, `approve_with_edits`, `reject` (reason required, fed back),
  `dismiss` (escape: agent stands down, waits for next customer message),
  `supersede` (a dispatcher reply or newer inbound message invalidated the
  pending draft). Superseded proposals are retained as history, never treated
  as the current response.
- **Dispatcher as participant:** the dispatcher can reply to the customer at
  any time (`POST /api/conversations/{id}/reply`), out the same shared Sender
  path. The API records queued delivery intent before attempting delivery and
  signals the run as labeled context. Decisions are synchronously persisted,
  actor-attributed, command-idempotent compare-and-set operations; the workflow
  signal wakes execution after the decision is already durable.
  There is no takeover mode and no "agent's turn".
- **Escalation is orthogonal to approval:** the auto-approved `escalate` tool
  answers "should a human engage *now*" (per conversation), never "should this
  action execute" (per action — that stays with Policy). It projects
  `attention_state: flagged → acknowledged` on the conversation; flagged sorts
  to the top of the dispatcher's list.

## 5. What is implemented (verified end-to-end)

### Residential-first correctness cutover (2026-07-10)

- Conversations bind to an exact `contact_identity_id` and are unique by
  `(org_id, channel_id, contact_identity_id)`. Outbound routing uses that exact
  identity. Customers, cases, and playbooks carry versions; cases also carry a
  durable summary.
- Inbound persistence is ordered by `event_seq` and `context_revision`. One
  transaction inserts the message and `conversation_event`, advances the
  cursor, supersedes a pending response, and writes a workflow-wakeup outbox
  row. Provider duplicates do not advance either cursor.
- Agent case handling is explicit: customer-wide candidate listing, explicit
  selection, explicit creation, and compare-and-set updates with exact source
  message IDs. The prompt and tools no longer select or reopen the latest case.
- Proposed actions carry action/context versions and customer/case/playbook
  dependency versions. Inbound wakes a workflow even while it is waiting on a
  draft, supersedes that draft, and replans. The loop rejects mixed or multiple
  effectful calls without executing any of them and uses an eight-completion
  turn budget.
- Dispatcher decisions are synchronous, actor-attributed by an
  `ActorProvider`, command-idempotent, and compare action/context versions
  under the conversation lock. Dispatcher replies use the same revision
  contract, persist queued delivery intent first, and expose delivery state.
- The detail API exposes exact identity, selected case, customer-wide candidate
  cases, current draft, superseded history, revisions, and delivery state. The
  UI renders one current response draft, case candidates, case corrections,
  conflict-safe decisions/replies, and blocked delivery states.

Migration 0011 also creates the durable seams for the remaining coordinator
cutover: `outbox`, `context_snapshots`, and `model_turns`. The active Temporal
workflow is still the legacy run-scoped loop; replacing it with
`ConversationCoordinatorV2`, processing outbox rows independently of the HTTP
process, persisting per-turn snapshots/model requests, and performing
delivery-dependent run completion remain the next implementation slice.

- **Infra:** docker-compose (Postgres 16 + Temporal dev server), `cmd/migrate`
  (12 migrations), `cmd/server` (JSON API :8080), `cmd/worker`. ULIDs
  everywhere; `org_id` on every table (seeded `org_dev`, `chan_dev`,
  `pb_field_service`).
- **agentkit:** full Action state machine with idempotent Postgres store;
  an activity-time `AgentResolver` seam (plus a static adapter for tests),
  Policy interface with RequireApproval default; append-only event log incl.
  per-completion `llm_completed` usage events (billing/eval substrate); JSON
  Schema validation of effective input at the ExecuteAction choke point;
  per-turn LLM budget (`MaxLLMCallsPerTurn`, exceed → event + app hook); run
  transcripts in Postgres (`run_messages`, workflow carries counters only);
  provider-agnostic LLM interface + Anthropic adapter (default
  `claude-opus-4-8`; unmodeled schema keywords pass through); temporalkit
  agent loop as above.
- **Field-service pack:** a code-owned registry describes inquiry,
  service-job, and locked quote lanes; live/coming-soon blocks; curated model
  tiers; tool risk text, defaults, settings, and floors. Pure pack functions
  validate writes and compute the same effective configuration used at
  runtime and in the API. The app resolver reads the run's playbook and org
  profile on each activity, then assembles model, rendered prompt, tools, and
  a lane-aware `ConfigPolicy`. Malformed persisted sections degrade to pack
  defaults instead of bricking the loop. Case resolution is explicit through
  `list_candidate_cases`, `select_case`, and `create_case`; `update_case`
  compare-and-sets the selected case with exact source-message provenance.
  `propose_response` is the single reviewed customer-facing unit and may carry
  delivery-dependent completion instructions. `escalate`, `stand_down`, and
  `wait_for_external` stop or pause work without pretending a customer message
  or external notification was sent. A fresh run receives customer, rolling
  summary, candidate-case, and recent-message context through `SystemContext`.
  `DISPATCH_FAKE_LLM=1` provides a scripted LLM for keyless demos/e2e,
  including a no-case hours inquiry whose delivered response completes its run.
- **Channels:** kind = code (`Adapter`), connection = data. Shared `Sender`
  persists delivery state and uses the action/message ID as its idempotency
  key. `Router` resolves an exact identity and transactionally inserts the
  deduped inbound message and conversation event, advances sequence/revision,
  supersedes the current draft, and enqueues workflow wakeup. Identity-bound
  thread creation is constraint-backed. The dev channel exercises this path.
- **API:** `POST /api/dev/inbound`, `GET /api/conversations[/{id}]`,
  `POST /api/actions/{id}/decision`, `POST /api/conversations/{id}/reply`,
  `POST /api/conversations/{id}/acknowledge`, `GET /api/runs/{id}/events`,
  `GET /api/stats/decisions` (per-tool outcome rates + human-decision latency),
  plus pack/playbook, organization-profile, and channel settings endpoints.
  Settings writes are org-scoped, actor-attributed, command-idempotent CAS
  operations; policy-floor violations return field-level 422 errors.
- **Web UI:** one app-wide sidebar promotes Inbox, Playbooks, Knowledge,
  Channels, and Insights. Inbox filters pending decisions and escalations,
  shows queue-wide pending count/worst wait, and opens the message thread with
  agent-draft review (approve / edit / revise / dismiss), case panel, customer
  simulator sheet and `/insights` decision metrics. Playbooks expose
  playbook lanes, approval policy with inline evidence, read-only actual
  models per journey stage, and voice controls,
  an honestly locked service catalog, channel routing/dev connections, and
  organization knowledge. Conversation surfaces poll (1.5–5s); settings fetch
  on mount and invalidate only after mutation.
- **Verified live** (scripted LLM + real Temporal/Postgres): full
  propose→decide→execute loop incl. edits and rejection-revision; worker
  restart mid-run resumes with pending action intact; persistent threads with
  run/case per task; playbook-driven agent + case-type selection; duplicate
  webhook delivery resolves to one message row and one agent turn; intake →
  delivery-confirmed completion → follow-up produces a briefed run; out-of-schema human
  edits fail validation and are fed back; transcripts persist per turn with
  workflow inputs reduced to counters.

Older decisions that still stand and their why, in brief: Temporal over a
hand-rolled state machine (durable multi-day waits are the hard part); UI
reads Postgres projections, never workflow queries; provider-agnostic LLM
interface kept to the tool-calling intersection; React SPA with the JSON API
as the contract.

## 6. Gaps to tackle next

The 2026-07-10 correctness cutover retires the earlier itemized race ledger.
The remaining work is grouped by the boundary it completes.

### 6.1 Correctness / durability

1. **Finish the coordinator cutover.** Activate `ConversationCoordinatorV2`,
   drain outbox wakeups independently of the HTTP process, persist context
   snapshots and model turns, and make run completion depend on confirmed
   delivery. The schema seams exist; the active workflow is still run-scoped.
2. **Prove replay and race behavior.** Add Temporal tests for inbound arriving
   during review, duplicate decisions/replies, supersession, delivery failure,
   workflow replay, and outbox retries. These invariants are the product.

### 6.2 Trust boundary

3. **Dispatcher impersonation via message text.** Dispatcher notes are a
   plain-text label inside a user turn; a customer typing the same label
   spoofs the dispatcher in the agent's context. Harmless while every
   `propose_response` is reviewed; a live hole once anything auto-sends. Fix
   structurally (delimit/escape customer text), not by prompt. (The briefing
   assembled by `app/briefing` labels message text as data — same treatment
   belongs on live turns.)
4. **Org scoping is on tables, not queries.** Every by-ID read
    (conversation, action, run, events) skips `org_id`; each new endpoint
    copies the pattern and the retrofit bill grows. Move store signatures to
    `(ctx, orgID, id)` now. (Also: CORS `*` is dev-only; don't ship it.)

### 6.3 Product gaps

5. **Escalation has no notification path.** The agent now accurately says that
   escalation only flags the queue and stands down. Add webhook/email paging
   before relying on it for unattended safety response.
6. **Conversation-list endpoint is N+1×4** (full `ListMessages` per
    conversation for a last-message preview plus per-row action scans),
    polled by every client. One query with lateral joins.
7. **Identity merge and verified profile editing are incomplete.** Exact
   routing is safe, and dispatchers can correct case data, but customer-level
   identity merge/dedup and audited profile correction remain product work.
8. **Dedicated review-queue surface is deferred.** Inbox provides
   Needs-decision, Escalated, and All filters, but bulk review and assignment
   need a purpose-built queue once operating volume justifies it.

### 6.4 Designed but not built

- **Hydration upgrades:** the rolling thread summary is currently the
  dispatcher-approved response-completion summary line (cheap, human-reviewed); an
  LLM-generated summary can replace it behind the same
  `conversations.thread_summary` column when threads outgrow five lines.
- **Pack SDK + second vertical** (was the future design 006): the delivered
  registry now owns lanes, prompt rendering, per-stage models, policy parameters,
  floors, and an honest catalog placeholder. Still missing are a registered
  case-schema/tool catalog that drives `update_case`, service-catalog wiring,
  and the pressure-test of a genuinely different second vertical.
- **Post-intake runs** (scheduling, follow-up): new task kinds + case
  lifecycle states on the same Action/Policy machinery — new agents, not new
  architecture. Unblocked by run-per-task and triage.
- **Auto-approval policy design:** per-org, per-lane Auto/Review/Forbid is now
  live and clamped by code-owned floors, with org-wide per-tool evidence
  inline. Follow-ups are lane-split decision stats and confidence/evaluation
  signals that can justify moving a floor rather than merely exposing a knob.
- Smaller deferred items: unified customer-profile UI, identity merge/dedup,
  auth/authz, real WhatsApp adapter (Meta Cloud API / Twilio), attention
  moving from thread to case grain, decision timeouts (holding messages),
  per-agent turn budgets on `AgentDefinition`, config-revision history UI,
  service-catalog-to-intake wiring.

### Suggested sequence

1. Finish the coordinator/outbox/model-turn cutover and its replay/race tests.
2. Delimit untrusted message text and complete org-scoped reads.
3. Add real escalation notification before an unattended pilot.
4. Remove the conversation-list N+1 queries; then build identity merge and
   audited customer-profile editing.

The WhatsApp adapter is unblocked from the persistence side, but should follow
the coordinator tests and real escalation notification before live customers
depend on it.
