# Dispatch — Implementation Overview & Roadmap

**Last updated:** 2026-07-09
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
    if a customer spoke:                 # dispatcher msgs inform, never trigger
        loop: LLM turn → for each tool call:
            ProposeAction → Policy.Evaluate
              → auto-approve | Forbid | durable wait for decision signal
            approved → ExecuteAction (the ONLY place a tool runs)
            feedback (result / rejection reason / edit note) → agent context
        until no more calls, terminal tool ran, or dismiss/supersede
    ContinueAsNew when history is large (messages carried in input)
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
  (8 migrations), `cmd/server` (JSON API :8080), `cmd/worker`. ULIDs
  everywhere; `org_id` on every table (seeded `org_dev`, `chan_dev`,
  `pb_field_service`).
- **agentkit:** full Action state machine with idempotent Postgres store;
  StaticPolicy (per-tool, RequireApproval default); append-only event log;
  provider-agnostic LLM interface + Anthropic adapter (default
  `claude-opus-4-8`); temporalkit agent loop as above.
- **Intake agent (the one pack):** `send_message` + `close_case` require
  approval; `update_case` + `escalate` auto-approved. `update_case` routes
  `customer_name` to the Customer and merges the rest into the case data bag.
  `DISPATCH_FAKE_LLM=1` scripted LLM for keyless demos/e2e.
- **Channels:** kind = code (`Adapter`), connection = data. Shared `Sender`
  (outbound) and `Router` (inbound: identity → thread → playbook → run →
  signal-with-start). The dev channel exercises the full production path.
- **API:** `POST /api/dev/inbound`, `GET /api/conversations[/{id}]`,
  `POST /api/actions/{id}/decision`, `POST /api/conversations/{id}/reply`,
  `POST /api/conversations/{id}/acknowledge`, `GET /api/runs/{id}/events`.
- **Web UI:** conversation list with pending badges + escalation flags,
  message thread with agent-draft review (approve / edit / revise / dismiss),
  case panel, customer simulator pane. Polling (1.5–3s).
- **Verified live** (scripted LLM + real Temporal/Postgres): full
  propose→decide→execute loop incl. edits and rejection-revision; worker
  restart mid-run resumes with pending action intact; persistent threads with
  run/case per task; playbook-driven agent + case-type selection.

Older decisions that still stand and their why, in brief: Temporal over a
hand-rolled state machine (durable multi-day waits are the hard part); UI
reads Postgres projections, never workflow queries; signals over Temporal
updates (revisit if decisions need synchronous validation); provider-agnostic
LLM interface kept to the tool-calling intersection; React SPA with the JSON
API as the contract.

## 6. Gaps to tackle next

From an adversarial review of the implementation (2026-07-09). Ordered within
each group; the sequence at the end is the recommendation.

### 6.1 Correctness / durability — prerequisites for a real WhatsApp adapter

1. **Duplicate outbound sends on activity retry.** A crash between
   `tool.Execute` (send happened) and `FinishAction` re-executes the tool on
   retry → customer gets the message twice. Fix: derive the outbound message
   ID from the action ID (`OutboundMessage.ID` seam already exists) so
   delivery is idempotent; carry it as the provider idempotency key.
2. **No inbound dedupe + run-creation race.** `Router.Receive` is three
   non-atomic steps and `InboundMessage` has no provider message ID — retried
   webhooks (WhatsApp retries and duplicates) create duplicate messages and
   turns. Two concurrent messages can create two live runs on one thread.
   Fix: add `ProviderMessageID`, key message inserts on it; enforce one live
   run per conversation.
3. **Temporal history grows O(n²).** Every `Complete` activity input embeds
   the whole transcript; a long conversation hits the ~2MB payload limit
   before ContinueAsNew helps. Fix: persist the run transcript in Postgres
   (append per turn), pass IDs to activities, assemble messages there. Fold
   into the context-hydration work (6.4) — same direction.
4. **Decision signals can be silently dropped.** A decision arriving for a
   non-pending action (supersede race, second dispatcher) is consumed and
   discarded with a log line after the API already returned 202. At minimum
   record dropped decisions as events; better, make 202 mean "recorded".
5. **Input schema validation doesn't exist** despite being a stated
   invariant. Human `edited_input` is only checked for valid JSON;
   `update_case` merges arbitrary top-level keys into the data bag; the
   Anthropic adapter silently drops schema keywords beyond
   properties/required. Validate at `ExecuteAction` against
   `Tool.InputSchema()` — one choke point.
6. **No loop budget, and LLM usage is discarded.** `agentTurn` can loop
   forever on auto-approved tools with no human in the path. Cap LLM calls
   per turn (exceed → escalate), and persist `llm.Usage` into the event log —
   it is the future billing/eval/cost substrate and can't be backfilled.

### 6.2 Trust boundary

7. **Dispatcher impersonation via message text.** Dispatcher notes are a
   plain-text label inside a user turn; a customer typing the same label
   spoofs the dispatcher in the agent's context. Harmless while every
   `send_message` is reviewed; a live hole once anything auto-sends. Fix
   structurally (delimit/escape customer text), not by prompt.
8. **Auto-approved tools are the injection surface.** `SetCustomerName`
   unconditionally overwrites the CRM name (the identity-create path
   deliberately only fills blanks — make them agree); `update_case` accepts
   any keys (see #5); `escalate` has no rate limit (pager DoS). Keep
   auto-approve, narrow the writes.
9. **`decided_by` is client-asserted** in the audit trail that *is* the
   product. Auth is a known non-goal for now, but when it lands, the decision
   path is its first consumer — not just reads.
10. **Org scoping is on tables, not queries.** Every by-ID read
    (conversation, action, run, events) skips `org_id`; each new endpoint
    copies the pattern and the retrofit bill grows. Move store signatures to
    `(ctx, orgID, id)` now. (Also: CORS `*` is dev-only; don't ship it.)

### 6.3 Product gaps

11. **The "second message" cliff.** After `close_case`, the next inbound
    ("when are you coming?") starts a *fresh intake run* that re-does intake.
    Needs a lightweight triage step — the fresh run's task should be "figure
    out what this message is about" (continue case / answer question / new
    case). `Router.Receive` + `run_bindings.task_kind` is the seam. Probably
    outranks the pack SDK in sequence.
12. **The dispatcher can't correct the case record.** `update_case` never
    surfaces for review (auto-approved) and the UI has no case editing — a
    wrong address has no human fix path. Case edits are also training-data
    signal the product thesis wants captured.
13. **Escalation has no notification path.** The tool description tells the
    model "this pages the dispatcher" — it only sorts a list in a UI nobody
    may have open, and the model calibrates its safety behavior on that
    claim. Build a real notification (webhook/email is enough) or make the
    description honest, before any pilot.
14. **Decision latency is uninstrumented** and it is the existential product
    risk (WhatsApp expectations vs. review queues). Two cheap projections
    over existing data: surface pending-action *age* loudly in the UI, and
    compute approval/edit/rejection rates per tool — the evidence the
    autonomy slider needs to move.
15. **Conversation-list endpoint is N+1×4** (full `ListMessages` per
    conversation for a last-message preview), polled by every client. One
    query with lateral joins.
16. **Test coverage doesn't match the claims.** ~150 lines of tests total;
    nothing covers action-state-machine idempotency under retries, the
    supersede/dismiss/decision races, or workflow replay (Temporal
    `testsuite` exists for exactly this). The pipeline is the product.

### 6.4 Designed but not built

- **Thread context hydration** (tiered briefing — was design 005, proposed):
  a new task-run starts cold today. Plan: per-run `SystemContext` seam in
  `AgentLoopInput` (agentkit stays generic); app-side assembler builds
  briefing = customer profile + rolling thread summary (regenerated at
  `close_case`, stored on the conversation) + recent message window, ordered
  stable-prefix-first for prompt caching. Ship profile+window first; fold in
  the transcript-persistence change from #3.
- **Pack SDK + second vertical** (was the future design 006): playbook
  `config` driving `update_case` schema, prompts, policy parameters; a
  registered tool catalog packs select from. Deliberately waits until a
  second vertical presses on the shape.
- **Post-intake runs** (scheduling, follow-up): new task kinds + case
  lifecycle states on the same Action/Policy machinery — new agents, not new
  architecture. Unblocked by run-per-task; sequenced after triage (#11).
- **Auto-approval policy design** (risk levels on tools, per-org policy
  config, confidence): the v2 slider turn. Needs #14's metrics first.
- Smaller deferred items: unified customer-profile UI, identity merge/dedup,
  auth/authz, real WhatsApp adapter (Meta Cloud API / Twilio), attention
  moving from thread to case grain, decision timeouts (holding messages).

### Suggested sequence

1. #1 + #2 (idempotent sends, inbound dedupe) — WhatsApp prerequisites.
2. #6 (usage logging + loop budget) — one small PR, buys the cost/eval story.
3. #11 (triage) — biggest product-quality delta per effort.
4. #5 (validation at ExecuteAction).
5. #14 (pending-age + approval-rate surfacing).
6. Hydration + #3 together (transcript in Postgres + briefing).

Everything else gets tracked as deliberate debt rather than surprises.
