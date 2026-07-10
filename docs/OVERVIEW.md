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
 ├─ Conversation        ONE persistent thread per (customer × connection);
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
  "is this work done". One thread per (customer, channel) — the customer
  view, not the thread, unifies channels.
- **Run ≠ conversation.** Runs come and go per task; a thread has many, at
  most one awaiting customer input. `run_bindings` keeps agentkit's `runs`
  table business-agnostic.
- **Playbook is the horizontal seam.** Connection → playbook → agent + case
  type. One pack exists (field service: `{address, issue, urgency}`); the
  pack SDK (config-driven schemas/prompts/policy) waits for a second vertical.

## 4. The agent loop

One Temporal workflow per run (`run-{runID}`), signals `inbound_message`,
`decision`, `dispatcher_message`. All non-determinism (LLM, DB, tool
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
            for each tool call:
              ProposeAction → Policy.Evaluate
                → auto-approve | Forbid | durable wait for decision signal
              approved → validate input vs schema → ExecuteAction
                         (the ONLY place a tool runs)
              feedback (result / rejection reason / edit note) → agent context
        until no more calls, terminal tool ran, or dismiss/supersede
    ContinueAsNew when history is large (input carries counters + the small
    unflushed delta; the transcript itself lives in run_messages)
```

- **Action lifecycle:** `proposed → pending_approval → approved /
  approved_with_edits / rejected → executing → completed / failed`. Decision
  kinds: `approve`, `approve_with_edits`, `reject` (reason required, fed back),
  `dismiss` (escape: agent stands down, waits for next customer message),
  `supersede` (dispatcher answered the customer directly while a draft was
  pending — the workflow records it, not the review endpoint).
- **Dispatcher as participant:** the dispatcher can reply to the customer at
  any time (`POST /api/conversations/{id}/reply`), out the same shared Sender
  path, recorded as an event and signaled into the run as labeled context.
  There is no takeover mode and no "agent's turn".
- **Escalation is orthogonal to approval:** the auto-approved `escalate` tool
  answers "should a human engage *now*" (per conversation), never "should this
  action execute" (per action — that stays with Policy). It projects
  `attention_state: flagged → acknowledged` on the conversation; flagged sorts
  to the top of the dispatcher's list.

## 5. What is implemented (verified end-to-end)

- **Infra:** docker-compose (Postgres 16 + Temporal dev server), `cmd/migrate`
  (10 migrations), `cmd/server` (JSON API :8080), `cmd/worker`. ULIDs
  everywhere; `org_id` on every table (seeded `org_dev`, `chan_dev`,
  `pb_field_service`).
- **agentkit:** full Action state machine with idempotent Postgres store;
  StaticPolicy (per-tool, RequireApproval default); append-only event log incl.
  per-completion `llm_completed` usage events (billing/eval substrate); JSON
  Schema validation of effective input at the ExecuteAction choke point;
  per-turn LLM budget (`MaxLLMCallsPerTurn`, exceed → event + app hook); run
  transcripts in Postgres (`run_messages`, workflow carries counters only);
  provider-agnostic LLM interface + Anthropic adapter (default
  `claude-opus-4-8`; unmodeled schema keywords pass through); temporalkit
  agent loop as above.
- **Intake agent (the one pack):** `send_message` + `close_case` require
  approval; `update_case` + `continue_case` + `escalate` auto-approved.
  `update_case` routes `customer_name` to the Customer and merges the rest
  into the case data bag (schema-constrained, no arbitrary keys). Triage:
  a fresh run on a thread with a prior case binds as `task_kind='triage'`,
  gets a briefing (customer + rolling thread summary + latest case + recent
  message window) via the per-run `SystemContext` seam, and can continue the
  prior case (`continue_case`), answer, or open a new one; `close_case` may
  complete a case-less run and rolls its approved summary into the thread's
  `thread_summary`. `DISPATCH_FAKE_LLM=1` scripted LLM for keyless demos/e2e.
- **Channels:** kind = code (`Adapter`), connection = data. Shared `Sender`
  (outbound; message ID derived from action ID = delivery idempotency key) and
  `Router` (inbound: identity → thread → message insert deduped on
  `provider_message_id` → playbook → live-run claim under a conversation lock
  → signal-with-start; the workflow dedupes signals by message ID). One thread
  per (customer, channel) and identity creation are constraint-backed. The dev
  channel exercises the full production path.
- **API:** `POST /api/dev/inbound`, `GET /api/conversations[/{id}]`,
  `POST /api/actions/{id}/decision`, `POST /api/conversations/{id}/reply`,
  `POST /api/conversations/{id}/acknowledge`, `GET /api/runs/{id}/events`,
  `GET /api/stats/decisions` (per-tool outcome rates + human-decision latency).
- **Web UI:** conversation list with pending badges (with oldest-pending age)
  + escalation flags, queue-wide worst wait in the header, message thread with
  agent-draft review (approve / edit / revise / dismiss), case panel, customer
  simulator pane, `/stats` decision-metrics page. Polling (1.5–5s).
- **Verified live** (scripted LLM + real Temporal/Postgres): full
  propose→decide→execute loop incl. edits and rejection-revision; worker
  restart mid-run resumes with pending action intact; persistent threads with
  run/case per task; playbook-driven agent + case-type selection; duplicate
  webhook delivery resolves to one message row and one agent turn; intake →
  close_case → follow-up produces a briefed triage run; out-of-schema human
  edits fail validation and are fed back; transcripts persist per turn with
  workflow inputs reduced to counters.

Older decisions that still stand and their why, in brief: Temporal over a
hand-rolled state machine (durable multi-day waits are the hard part); UI
reads Postgres projections, never workflow queries; signals over Temporal
updates (revisit if decisions need synchronous validation); provider-agnostic
LLM interface kept to the tool-calling intersection; React SPA with the JSON
API as the contract.

## 6. Gaps to tackle next

From an adversarial review of the implementation (2026-07-09). Numbering is
stable — code comments cite these numbers — so resolved items are ledgered,
not renumbered.

**Resolved 2026-07-10** (one commit each; details in git history): #1
idempotent outbound sends, #2 inbound dedupe + one live run per thread, #3
transcripts in Postgres, #5 input validation at ExecuteAction, #6 usage
logging + loop budget, #11 triage runs with briefing, #14 pending-age +
per-tool decision rates, and hydration's first tiers (§6.4: profile + rolling
summary + recent window via the `SystemContext` seam).

Known accepted races (documented, deliberately unfixed): a webhook retry that
arrives *after* the original message's run already completed can spawn a
spurious triage run (it gets a briefing, so it degrades gracefully); a
customer message landing in the exact window where a terminal `close_case`
executes stays buffered in a workflow that then returns — the message is
persisted on the thread but no run processes it until the customer's next
message. Both are narrow; revisit with decision timeouts.

### 6.1 Correctness / durability

4. **Decision signals can be silently dropped.** A decision arriving for a
   non-pending action (supersede race, second dispatcher) is consumed and
   discarded with a log line after the API already returned 202. At minimum
   record dropped decisions as events; better, make 202 mean "recorded".

### 6.2 Trust boundary

7. **Dispatcher impersonation via message text.** Dispatcher notes are a
   plain-text label inside a user turn; a customer typing the same label
   spoofs the dispatcher in the agent's context. Harmless while every
   `send_message` is reviewed; a live hole once anything auto-sends. Fix
   structurally (delimit/escape customer text), not by prompt. (The briefing
   assembled by `app/briefing` labels message text as data — same treatment
   belongs on live turns.)
8. **Auto-approved tools are still an injection surface.** `SetCustomerName`
   unconditionally overwrites the CRM name (the identity-create path
   deliberately only fills blanks — make them agree); `escalate` has no rate
   limit (pager DoS). update_case's arbitrary-keys hole is closed (#5). Keep
   auto-approve, narrow the writes.
9. **`decided_by` is client-asserted** in the audit trail that *is* the
   product. Auth is a known non-goal for now, but when it lands, the decision
   path is its first consumer — not just reads.
10. **Org scoping is on tables, not queries.** Every by-ID read
    (conversation, action, run, events) skips `org_id`; each new endpoint
    copies the pattern and the retrofit bill grows. Move store signatures to
    `(ctx, orgID, id)` now. (Also: CORS `*` is dev-only; don't ship it.)

### 6.3 Product gaps

12. **The dispatcher can't correct the case record.** `update_case` never
    surfaces for review (auto-approved) and the UI has no case editing — a
    wrong address has no human fix path. Case edits are also training-data
    signal the product thesis wants captured.
13. **Escalation has no notification path.** The tool description tells the
    model "this pages the dispatcher" — it only sorts a list in a UI nobody
    may have open, and the model calibrates its safety behavior on that
    claim. Build a real notification (webhook/email is enough) or make the
    description honest, before any pilot. The turn-budget hook (#6) now
    reuses the same attention projection — a notification path serves both.
15. **Conversation-list endpoint is N+1×4** (full `ListMessages` per
    conversation for a last-message preview plus per-row action scans),
    polled by every client. One query with lateral joins.
16. **Test coverage doesn't match the claims.** Unit tests cover policy
    routing, rejection-feedback recognition, and schema validation, but
    nothing covers action-state-machine idempotency under retries, the
    supersede/dismiss/decision races, or workflow replay (Temporal
    `testsuite` exists for exactly this). The pipeline is the product.

### 6.4 Designed but not built

- **Hydration upgrades:** the rolling thread summary is currently the
  dispatcher-approved `close_case` summary line (cheap, human-reviewed); an
  LLM-generated summary can replace it behind the same
  `conversations.thread_summary` column when threads outgrow five lines.
- **Pack SDK + second vertical** (was the future design 006): playbook
  `config` driving `update_case` schema, prompts, policy parameters; a
  registered tool catalog packs select from. Deliberately waits until a
  second vertical presses on the shape.
- **Post-intake runs** (scheduling, follow-up): new task kinds + case
  lifecycle states on the same Action/Policy machinery — new agents, not new
  architecture. Unblocked by run-per-task and triage.
- **Auto-approval policy design** (risk levels on tools, per-org policy
  config, confidence): the v2 slider turn. Its evidence now exists
  (`/api/stats/decisions`); design when a few weeks of real decisions
  accumulate.
- Smaller deferred items: unified customer-profile UI, identity merge/dedup,
  auth/authz, real WhatsApp adapter (Meta Cloud API / Twilio), attention
  moving from thread to case grain, decision timeouts (holding messages),
  per-agent turn budgets on `AgentDefinition`.

### Suggested sequence

1. #13 (real escalation notification) — before any pilot; the model's safety
   behavior is calibrated on the tool description being true.
2. #7 + #8 (delimit customer text; narrow SetCustomerName; rate-limit
   escalate) — the trust-boundary trio, small and mostly mechanical.
3. #4 (record dropped decisions as events).
4. #10 (org-scoped store reads) + #16 (pipeline tests) — hygiene that gets
   more expensive every week it waits.
5. #15 (list-endpoint query), #12 (case editing in the UI).

The WhatsApp adapter is unblocked from the durability side (#1/#2/#3 done);
it should land after 1–2 above so escalation and the trust boundary are
honest before real customers hit them.
