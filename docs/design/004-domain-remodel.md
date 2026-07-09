# Design 004: Domain Remodel — Customers, Conversations, Cases, Runs, and Playbooks

**Status:** Accepted — supersedes the v1 domain shape; implementation phased (§13)
**Date:** 2026-07-08
**Scope:** Pull apart the four concepts the v1 model collapsed into a single
1‑to‑1 chain — *who the customer is*, *the ongoing thread with them*, *a unit of
work*, and *an agent execution* — and introduce **Playbooks** as the seam along
which the product generalizes to new verticals (field service now; clinics,
orders later) **as code packs selected by config**, never a no‑code workflow
engine.

Builds on `000-foundation.md`, `002-organization-and-channels.md`, and
`003-dispatcher-as-participant.md`. It **changes** several v1 decisions; each is
called out explicitly in §11. It touches **only `app/`** — agentkit, the Action
pipeline, the event log, and the Temporal agent loop are unchanged (§10).

---

## 1. Why now: the 1‑to‑1 chain

v1 shipped a model that reads, structurally, as one rigid chain:

```
Customer (= a phone number)  ──1:1──  Conversation  ──1:1──  Job
                                            │
                                          1:1  Run
```

The evidence, in code:

- `customers UNIQUE(org_id, phone)` — a customer *is* a phone number.
- `jobs.conversation_id UNIQUE` — exactly one job per conversation.
- `conversations.run_id` + `GetConversationByRunID` — one run per conversation.
- `CompleteIntake` closes the conversation when the job finishes; the next
  inbound message creates a **brand‑new** conversation (`OpenConversationForCustomer`
  only matches `status='open'`).

This was correct for the intake demo and is wrong for the product. Four distinct
things are fused, and every limitation below is a symptom of the fusion:

| Symptom | Root fusion |
|---|---|
| A customer can't be recognized across WhatsApp + SMS + email | Customer = a single phone identity |
| No unified inbox / customer profile spanning channels | same |
| A customer can't have multiple jobs in one thread; a job can't span threads | Job = Conversation |
| Threads reset on every completed job (history lost — `003` §8) | Conversation = Job lifecycle |
| Nowhere for post‑intake work (schedule, follow‑up) to live | Run = Conversation, ends at `close_job` |
| "Job" doesn't fit appointments or orders | the unit of work is named for one vertical |

This doc pulls the four apart and, in doing so, gives the product its missing
substrate for work *after* intake and its seam for *other verticals*.

## 2. The decomposition

Four concepts, each with its own lifetime and cardinality:

```
Organization
 ├─ Playbook            a vertical pack: case type + schema + toolset/policy + prompt + lifecycle
 ├─ ChannelConnection   ── default_playbook          (the routing binding 002 §10 reserved)
 ├─ Customer            the CRM aggregate (a person / business the org serves)
 │   └─ ContactIdentity (channel_kind, address)      one customer, many identities
 ├─ Conversation        one persistent thread per (customer × channel)
 │   └─ Message         (author, direction, body)
 └─ Case                a unit of work; MANY per customer; the generalization of "Job"
     └─ Run(s)          one durable agent *task*; many over a case's life
```

The rule that keeps this honest, carried from `002` §1 and reaffirmed by the
product‑scope decision behind this doc:

> **Verticals are code, selected and parameterized by config — never authored by
> orgs.** Code owns the verbs (tools, adapters, case lifecycles, the agent loop)
> and the nouns' shapes. Config owns *which* pack is active, *which* fields and
> tools it exposes, under *what* policy, in *whose* voice. A genuinely new
> vertical is new code surfaced as a config selection, not a canvas.

## 3. (a) Customer & ContactIdentity — omnichannel + unified inbox

Promote `Customer` from "a phone number" to the CRM aggregate `002` §10
deferred, and split contact endpoints out:

```go
type Customer struct {
    ID        string
    OrgID     string
    Name      string
    // notes, tags, etc. graduate in as they earn it (typed-core-plus-bag)
    CreatedAt time.Time
}

type ContactIdentity struct {
    ID          string
    OrgID       string
    CustomerID  string
    ChannelKind string // "dev" | "whatsapp" | "sms" | "email"
    Address     string // phone, email, dev token — the customer-side address
    CreatedAt   time.Time
}
```

- Uniqueness moves off `Customer` and onto the identity:
  `UNIQUE(org_id, channel_kind, address)`. `customers UNIQUE(org_id, phone)` is
  gone; `phone` leaves `customers` entirely.
- **Inbound resolution** becomes `(kind, address) → ContactIdentity → Customer`.
  The `Router` (`002` §5) already receives the connection (hence `kind`) and the
  `From` address — it now resolves the identity, get‑or‑creating the customer +
  identity together, instead of get‑or‑creating a phone‑keyed customer.
- **Unified inbox** falls out for free: the inbox lists conversations, but every
  conversation resolves to one `Customer`, whose profile aggregates *all* their
  threads, cases, and history across channels. We do **not** merge transports
  into one thread — a WhatsApp thread and an SMS thread stay separate
  conversations (a reply has to go out *somewhere*); the *customer view* is what
  unifies them.

Merging two identities discovered to be the same person (dedup) is a real CRM
operation but is deferred — the split is the precondition; the merge tool is its
own small follow‑up.

## 4. (b) Conversation as a persistent thread

A `Conversation` becomes the durable messaging thread with a customer on a
channel — **one per (customer, channel connection)** — and is **never
auto‑closed on case completion**. It is the inbox primitive.

- `CompleteIntake` no longer closes the conversation; it only advances the
  *case* (§5). The thread persists.
- `Status open|closed` is replaced by a thread‑level notion (`active`, and an
  `archived` state for UI hygiene only — never a lifecycle gate). The
  case, not the thread, now carries "is this work done."
- The messages table is the **durable source of truth for thread history** — see
  §7. This is what closes the `003` §8 gap ("a later message starts a fresh run
  without prior context"): the thread outlives any run, so context is always
  reconstructable.

## 5. (c) Case — "Job" generalized

Rename and generalize `Job` into `Case`: a unit of work the org is fulfilling
for a customer, with a lifecycle, **many per customer**, decoupled from any one
thread.

```go
type Case struct {
    ID             string
    OrgID          string
    CustomerID     string          // references the customer (not a copied snapshot)
    PlaybookID     string          // the pack that defines this case's shape & lifecycle
    Type           string          // "field_service_job" (pack #1); "appointment"/"order" later
    Status         string          // generic core states, refined per-pack lifecycle (§8)
    Data           json.RawMessage // playbook-defined structured fields (typed-core-plus-bag)
    ConversationID string          // the thread it was raised in (nullable; a link, not an owner)
    CreatedAt      time.Time
    UpdatedAt      time.Time
}
```

Design points:

- **Neutral core noun.** "Case" is the umbrella over *job* (field service),
  *appointment* (clinic), *order* (bakery/florist). The UI renders a per‑playbook
  **display label** ("Job" / "Appointment" / "Order"); the schema keeps one noun.
- **Typed‑core‑plus‑bag**, exactly the discipline `002` §11 named *for this
  schema*. A few universal columns (customer, status, timestamps) stay typed; the
  vertical‑specific fields live in `Data`, validated against the **playbook's
  schema**. v1's hardcoded `Job{customer_name, address, issue, urgency}` becomes
  the field‑service pack's `Data` schema — `{address, issue, urgency}` — with
  `customer_name` living on the `Customer`, not copied.
- **Reference, not snapshot.** The case points at the customer; contact details
  live in one place. (If a "what was true at intake" snapshot is ever wanted,
  that is a deliberate, separate field — not the accidental copy v1 has in
  `UpsertJob`.)
- **Cardinality unlocked.** `UNIQUE(conversation_id)` is dropped. One thread can
  raise several cases ("my sink *and* my heater"); one case can be discussed
  across threads over time.

## 6. (d) Run per task, not per conversation

This answers `000` §11's open question ("run per‑conversation, per‑job, or
per‑agent‑task? Likely per‑task with a coordinating parent").

A **Run is one durable agent task** — intake for a new case, scheduling a case, a
follow‑up, a reschedule — not the lifetime of a thread. Runs come and go; the
conversation and case persist across many of them. Concretely:

- A run is bound to `(conversation, case, playbook‑task)`. Because agentkit's
  `runs` table is business‑agnostic (`id, org_id, agent, status`), the app owns
  this binding in an **app‑side table** (`run_bindings` or similar) rather than
  adding domain columns to agentkit — preserving the `000` §3 boundary.
- **`conversations.run_id` (a single slot) is removed.** A thread no longer has
  "the" run; it has a history of runs, at most one *awaiting customer input* at a
  time.

**Routing — the proportionate version.** We do **not** build a per‑conversation
supervisor workflow yet. The `Router.Receive` path already has the seed of this
(`ensureRun` starts a fresh run when the last finished). It generalizes to:

```
inbound (kind, address)
  → resolve identity → customer → the (customer, channel) thread  (get-or-create)
  → append message to the thread
  → is a run awaiting customer input on this thread?
        yes → signal it (inbound_message)
        no  → start a new task-run under the connection's default playbook
```

A heavyweight **conversation‑supervisor** Temporal workflow (parent that spawns
child task workflows) is the escalation path for *concurrent* tasks and
*proactive/scheduled* agent turns. We defer it until a second kind of run
(scheduling, follow‑up) actually forces concurrency — the same "don't abstract
before the second instance" discipline the repo applies to agentkit extraction
(`000` §10) and to lifting primitives into agentkit (`001` §5, `002` §8).

## 7. Context assembly from the thread

v1 keeps conversation history in **workflow state** (`AgentLoopInput.Messages`,
carried across `ContinueAsNew`). With per‑task runs, a new run must reconstruct
what it needs from durable storage, not inherit it from a workflow that has
ended.

- **Postgres (`messages` + the case's `Data`) is the durable context;** workflow
  state is just the *active task's* working buffer.
- Starting a task‑run gets a **context‑assembly activity**: "given this thread
  and this case, produce the initial `[]llm.Message` (recent thread history,
  relevant case state, task framing)." This is where dispatcher messages (`003`)
  come along for free via `author`, and where history windowing / summarization
  lands later.

This is a clarifying change, not just a mechanical one: it names *what a run
sees* as an explicit, testable seam instead of an implicit consequence of
workflow continuation.

## 8. Playbooks — vertical packs on a shared core

The Playbook is the seam that makes the product horizontal **without** becoming a
workflow engine. Per the scope decision behind this doc: **build the generic
Case + Playbook substrate now; ship exactly one pack (field service); add
clinics/orders as code packs later.**

A **Playbook** bundles what a vertical needs, with a clear code/config split:

| A playbook defines | Field‑service pack (the only one now) | code / config |
|---|---|---|
| Case type + lifecycle states | `intake → open → scheduled → done` | **code** (per pack) |
| Case `Data` schema | `{address, issue, urgency}` | code shape · config: which fields, labels, required |
| Active toolset + policy | `send_message`, `update_case`, `escalate`, `close_case` | **code** (verbs) · config: on/off, thresholds |
| Prompt / voice | the plumbing‑intake system prompt | **config** |
| Completion criteria | name + address + issue + urgency + recap | config over a code‑defined check |

Consequences for existing code:

- **`update_job` → `update_case`.** Its `InputSchema()` is no longer a hardcoded
  literal; it is **derived from the active playbook's `Data` schema.** The `Tool`
  interface already returns `InputSchema()` — we make the intake tools
  playbook‑parameterized (constructed with the playbook, or reading it from
  `RunContext`). `close_job → close_case`; `send_message`, `escalate` are pack‑
  agnostic and stay.
- **A tool catalog.** Tools become a registered catalog the app owns; a playbook
  *selects and parameterizes* a subset. This is selection over code‑defined verbs
  — not orgs wiring logic.
- **"Conditions," disciplined.** Org‑configurable conditions (when to
  auto‑approve, when a field is required, when to escalate) are expressed as
  **policy parameters** over `Policy.Evaluate`'s existing evaluation points — not
  as user‑authored branching. This keeps `000` principle 6 (HITL is policy) and
  the `002` §1 line intact.
- **Selection binding.** `ChannelConnection` (or org) gains a
  `default_playbook`; the `Router` stops using the global `r.agent`/`s.AgentName`
  and routes inbound to the connection's playbook. This is the binding point
  `002` §10 reserved.

The full multi‑pack mechanics (how a second pack registers its lifecycle, how
`Data` schemas are declared, how policy config is shaped) are **not fully
specified here** — deliberately. With one pack, any such API is a guess (same
reasoning as `000`'s agentkit‑extraction trigger). This doc gives Playbook its
*place and role*; the pack SDK is `005` once the field‑service pack is real and a
second vertical presses on its shape.

## 9. Where post‑intake work finally lives

Today `close_job` ends the run and **nothing further happens** — ironic for a
product called Dispatch. The persist‑the‑case model is the missing substrate:

- Intake run completes → the **Case is `open`, not gone.**
- A **scheduling run** (a different playbook task, same case + thread) proposes
  slots, messages the customer, books.
- A **follow‑up run** checks in after the work is done.
- Each reuses the *same* Action / Policy / Run machinery — the hard part is
  already built. Going "past the initial conversation" is **new agents and case
  lifecycle states, not new architecture.**

**Dispatching activities are extensible verbs, not just intake.** The product
name "Dispatch" stays, and the technical model must let us *add dispatching
activities* beyond field‑service intake — booking an appointment, creating an
order — as first‑class capabilities. The seam for that is deliberately the one
already in place: **the tool catalog.** A dispatching activity is a `Tool`
(`book_appointment`, `create_order`, `schedule_visit`) that a playbook's toolset
includes, executed through the **same Action → Policy → execute pipeline** as
`send_message` today, so every appointment booked or order created is proposed,
reviewed (or auto‑approved by policy), executed, and audited exactly like any
other action. A new vertical therefore adds three things and nothing else: a
**case type + lifecycle** (§8), a **`Data` schema**, and the **fulfillment
tools** its playbook exposes. No change to the pipeline, the event log, or the
loop — which is the whole point of keeping fulfillment a verb rather than a
bespoke flow.

## 10. Boundary: still app‑only

Per the agentkit test ("would a non‑dispatch agent business need this?"):
`Customer`, `ContactIdentity`, `Conversation`, `Case`, and `Playbook` are all
**app**. A generic agent business wants *identity*, *threads*, *work items*, and
*configurable variants* too — but the shapes here (CRM customers, per‑channel
threads, dispatch/appointment/order cases, dispatch playbooks) are domain. As
with escalation (`001` §5) and channels (`002` §8), we do **not** lift a
speculative "entity / work‑item / variant" primitive into agentkit before a
second business proves its shape. **agentkit, the Action pipeline, the event
log, and the Temporal agent loop are untouched by this doc** — the payoff of the
original layering.

The one seam this doc *adds inside agentkit's neighborhood* is app‑side: the
`run_bindings` table (§6) and the context‑assembly activity (§7) both live in
`app`, keyed on domain concepts agentkit never sees.

## 11. Changes to earlier decisions, stated explicitly

Per the numbered‑doc convention:

1. **Job ↔ Conversation 1:1 (`000` §8, migration `0001`).** Replaced by `Case`,
   many per customer, decoupled from the thread (`UNIQUE(conversation_id)`
   dropped). "Job" is now the field‑service pack's display label over `Case`.
2. **Run ↔ Conversation 1:1 (`000` §4/§5).** A run is one agent *task*, not a
   thread's lifetime. `conversations.run_id` is removed; the app owns
   run↔(case, conversation) binding.
3. **Conversation lifecycle (`000` §8, `CompleteIntake`).** The thread is
   persistent and no longer closed on completion; the *case* carries "done."
4. **Customer = phone (`000` §8, `002` §10 "Customer stays thin").** Promoted to
   the CRM aggregate now, with `ContactIdentity` splitting out contact endpoints;
   `customers UNIQUE(org_id, phone)` → `contact_identities UNIQUE(org_id, kind,
   address)`.
5. **Playbook selection (`002` §10, `Router.agent`/`s.AgentName` global).** The
   connection's `default_playbook` selects the playbook; the global goes away.
6. **`003` §8 open items** ("reply on a finished run", "runs don't replay full
   history") are resolved structurally by persistent threads + context assembly
   (§4, §7).

Everything else in `000`–`003` stands: the Action lifecycle, Policy, the
append‑only event log, escalation's orthogonality to approval, and the
dispatcher‑as‑participant model all carry over unchanged (a dispatcher is a
participant in a *thread*; escalation attention may move from thread to case —
§14).

## 12. Migration & compatibility

All additive/rename migrations; existing seeded data (`org_dev`, `chan_dev`)
keeps working with no manual steps.

- **`contact_identities`** new table; backfill one identity per existing customer
  from `(kind = the customer's conversation's connection kind, address = phone)`.
  Drop `customers.phone` and `UNIQUE(org_id, phone)` after backfill.
- **`conversations`**: drop the close‑on‑complete behavior; replace `status
  open|closed` with `active|archived` (default `active`); remove `run_id`.
- **`jobs → cases`**: rename; add `customer_id` (backfill via
  `conversation → customer`), `playbook_id` + `type` (backfill to the seeded
  field‑service pack / `field_service_job`), `data JSONB` (backfill
  `{address, issue, urgency}` from the typed columns), keep `conversation_id`
  nullable and drop its `UNIQUE`.
- **`playbooks`** new table; seed one field‑service playbook and point
  `chan_dev.default_playbook` at it.
- **`run_bindings`** new app table: `(run_id, org_id, conversation_id, case_id,
  playbook_id, task_kind)`.
- Keep `update_job`/`close_job` tool *names* working for one release if any
  scripts depend on them, but internally they are `update_case`/`close_case`.

## 13. Phasing

The four decompositions are interdependent but land in a safe order:

- **Phase 1a — Identity split (CRM spine).** ✅ *Done.* `Customer`/`ContactIdentity`
  split; inbound resolves `(kind, address) → identity → customer`; the API
  surfaces a thread's contact address so the UI keeps working. Lowest risk, fully
  decoupled, and the precondition for a customer spanning channels.
- **Phase 2 — Case generalization.** ✅ *Done.* `Job → Case`: neutral record with a
  typed core + a per‑vertical `Data` bag, a `type` discriminator, and a
  `customer_id` reference (name → customer, contact → identity, no longer copied).
  `update_case`/`close_case` replace `update_job`/`close_job`; the `data` merge is
  schema‑agnostic in the store (so a playbook‑driven schema needs no store
  change), while `update_case`'s *input schema* stays hardcoded to the
  field‑service fields until Phase 4. **Transitional:** the case stays 1:1 with the
  conversation (`UNIQUE(conversation_id)` kept) and threads still close on
  completion — because *which case a message belongs to* is only well‑defined once
  runs bind to cases, which is Phase 3.
- **Phase 3 — Persistent threads + run per task (coupled).** ✅ *Done (structural).*
  Dropped `UNIQUE(conversation_id)` (many cases per thread); made conversations
  durable (stop closing on completion); removed `conversations.run_id`; added
  `run_bindings` binding a run to `(conversation, case, task)`. The case binds to
  the run on first `update_case`, so multi‑case per thread has a well‑defined
  "which case is active." **Context hydration is deferred** to a clean follow‑up
  (a new task‑run still starts without prior thread history — the pre‑existing
  003 §8 limitation): it needs faithful transcript assembly *and* handling for the
  scripted fake‑LLM's user‑turn counting, which don't belong rushed into the
  structural change. The **unified customer‑profile UI** (grouping a customer's
  threads/cases) is also its own follow‑up; the data model now supports it.
- **Phase 4 — Playbook substrate.** ✅ *Done (selection seam).* `playbooks` table
  (selects the code agent/pack + names the case type it produces), seeded
  `pb_field_service`. A channel connection carries a `default_playbook_id`; the
  Router resolves the playbook, runs its agent, and records `run_bindings.playbook_id`
  so the case type is *derived from the playbook* rather than hardcoded. With one
  pack this is the real selection binding — the point the horizontal story hangs on.
  **Deferred to Phase 5 (the pack SDK):** `update_case`'s input schema / prompts /
  policy driven by playbook `config` (the store's `data` merge is already
  schema‑agnostic, so no store change is owed); a config‑authored tool catalog.
- **Phase 5 (own doc, 005) — Pack SDK + second vertical.** Only when a second
  vertical is real. Also unlocks the first post‑intake task‑run (scheduling),
  which validates §6/§9.

Each phase is shippable and reversible on its own; nothing after Phase 1 is on
the critical path to the omnichannel inbox the product needs first.

## 14. Open questions

- **Attention grain.** Escalation/attention is projected on the *conversation*
  (`001`). With multiple cases per thread, "which case is the emergency?" blurs;
  attention likely wants to live on the *case* or *run*, with the thread
  surfacing the max. Deferred until multi‑case threads are real.
- **Case ↔ thread linkage strength.** Is `ConversationID` on a case enough, or do
  we want a case‑to‑message association (which messages discussed which case)?
  Leaning: start with the single link; add message‑level association only if the
  UI needs it.
- **Identity merge / dedup.** The split enables it; the merge tool (and its audit
  trail) is a separate follow‑up.
- **Per‑channel vs. per‑customer thread.** §4 chooses one thread per (customer,
  channel). If a customer moves WhatsApp→SMS mid‑issue, do we ever want one
  logical thread spanning transports? Deferred; the customer view already unifies
  the display.
- **Product naming.** "Dispatch" presupposes field service. If the horizontal
  direction holds past a second pack, the name will strain. Noted, not acted on.
- **Pack lifecycle declaration.** How a pack declares its case states and
  transitions (code enum? a small declarative table the code owns?) is the
  central `005` question and intentionally unanswered here.
