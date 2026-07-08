# Design 000: Foundation — AI-Native Dispatch

**Status:** Implemented (v1 intake demo) — see `docs/STATUS.md` for the
build-out checklist
**Date:** 2026-07-05
**Scope:** Overall architecture, core primitives, and the v1 WhatsApp-intake demo.

Future features get their own numbered docs in `docs/design/`. This doc is the
foundation they build on; when a later doc changes a decision made here, it
should say so explicitly.

---

## 1. Vision

Dispatch software (inspired by probook.ai) for field-service businesses, built
AI-native: agents do the work — intake, scheduling, follow-up — and humans
review it, the way a developer reviews a coding agent's actions.

Three product principles drive the architecture:

1. **Agent actions are modeled like a coding agent's tool calls.** Every
   externally-visible thing an agent does is an explicit, typed *Action* that a
   human can approve, reject, or edit before it executes.
2. **Human-in-the-loop is a policy, not an architecture.** The system starts
   with a human approving everything, and ends with the work disappearing. The
   only thing that changes along the way is the *approval policy* — never the
   shape of the system. (Claude Code's permission modes — ask → accept-edits →
   auto — are the reference model.)
3. **The agent machinery is a reusable layer.** The primitives for running,
   approving, tracking, and auditing agents are business-agnostic. Dispatch is
   the first application built on them, not the only one.

## 2. The core insight: Action + Policy

The unit everything revolves around is the **Action**:

> An agent proposes an Action. A Policy decides whether it needs human
> approval. A human (or the policy) decides. Approved actions execute; the
> result — including rejections and edits — feeds back into the agent's
> context.

This one loop covers the whole trajectory of the product:

| Phase | Policy | Human experience |
|---|---|---|
| v1 (now) | Everything requires approval | Reviews every draft reply |
| v2 | Auto-approve low-risk action types; the rest require approval | Reviews the interesting 20% |
| v3 | Auto-approve by default; escalate on low confidence or policy triggers | Handles exceptions only |

Nothing structural changes between phases. Every action — auto-approved or
not — flows through the same pipeline and lands in the same audit log. Human
edits and rejections are recorded with reasons, which is exactly the data
needed to justify (and eventually learn) auto-approval.

## 3. Layered architecture

```
┌──────────────────────────────────────────────────────┐
│  app/          Dispatch application                  │
│                jobs, customers, conversations,       │
│                channels (WhatsApp), intake agent,    │
│                web UI, HTTP API                      │
├──────────────────────────────────────────────────────┤
│  agentkit/     Foundational agent library            │
│                runs, actions, decisions, policies,   │
│                tools, event log, LLM abstraction,    │
│                Temporal workflow patterns            │
├──────────────────────────────────────────────────────┤
│  Temporal      Durable execution                     │
│  Postgres      App state + event projections         │
└──────────────────────────────────────────────────────┘
```

**Dependency rule: `agentkit` never imports `app`.** agentkit knows nothing
about dispatch, jobs, or WhatsApp. The app defines domain tools, prompts, and
channels, and hands them to agentkit. This is the seam along which agentkit
gets extracted into its own repo/module when a second business needs it — we
get the discipline of the split without paying the multi-repo tax while the
primitives are still finding their shape.

What lives where (the test: *"would a non-dispatch agent business need
this?"* → agentkit):

| agentkit | app |
|---|---|
| Action lifecycle & state machine | Tool implementations (send WhatsApp reply, update job) |
| Approval policies & decision recording | Which tools each agent gets, prompts |
| Agent loop as a Temporal workflow pattern | Domain workflows (intake conversation) |
| LLM provider abstraction | Provider selection & prompt content |
| Append-only event log & projections | Domain tables (jobs, customers, messages) |
| Run tracking (status, history, queries) | Web UI, HTTP API, channel adapters |

## 4. Core primitives (agentkit)

### Action

An Action is one proposed tool call, with a full lifecycle:

```
                 ┌── policy: auto ──────────────┐
proposed ── policy ──► pending_approval ──► approved ──► executing ──► completed
                              │                                  └───► failed
                              ├──► approved_with_edits ──► executing ...
                              └──► rejected  (reason fed back to agent)
```

```go
type Action struct {
    ID             ActionID
    RunID          RunID
    Tool           string          // tool name, e.g. "send_message"
    Input          json.RawMessage // what the agent proposed
    EditedInput    json.RawMessage // set when decision = approved_with_edits
    State          ActionState
    Decision       *Decision       // who/what decided, when, why
    Result         json.RawMessage // execution output, fed back to the agent
    ProposedAt     time.Time
    DecidedAt      *time.Time
}

type Decision struct {
    Kind      DecisionKind // approve | approve_with_edits | reject
    DecidedBy string       // user ID, or "policy:auto"
    Reason    string       // required for reject; free text otherwise
}
```

Invariants:

- **Every action goes through the pipeline.** There is no side door for
  executing a tool without an Action record — even auto-approved ones. This is
  what makes the audit trail trustworthy and the HITL→autonomous slider real.
- **Original input is never overwritten.** Edits are stored alongside, so we
  always know what the agent proposed vs. what actually ran. (Original + edit
  pairs are future training/eval data.)
- **Rejections and edits feed back into the agent's context** ("the dispatcher
  rejected this because…", "the dispatcher edited your draft to…"), so the
  agent revises rather than repeats — same as a coding agent.

### Tool

A tool is a capability the agent can invoke. agentkit defines the interface;
the app implements it:

```go
type Tool interface {
    Name() string
    Description() string          // shown to the LLM
    InputSchema() json.RawMessage // JSON Schema, validated before execution
    Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
}
```

(A risk-level classification on tools was considered and cut for v1 — the
static per-tool policy table covers the demo. It returns with the
auto-approval design doc, where the policy needs something to key on.)

### Policy

```go
type PolicyDecision int // AutoApprove | RequireApproval | Forbid

type Policy interface {
    Evaluate(ctx context.Context, a Action) PolicyDecision
}
```

v1 ships one implementation: a static per-tool table with
`RequireApproval` as the default. The interface is where confidence scores,
per-customer settings, and learned policies land later — without touching the
agent loop.

### Run

A Run is one durable agent execution — for dispatch v1, one intake
conversation from first inbound message to job closed. A Run has an agent
definition (prompt + tool set + policy), a status, and an ordered history of
events.

### Event log

Everything that happens in a run is appended to an `events` table:
`message_received`, `action_proposed`, `decision_made`, `action_executed`,
`run_completed`, … Events are the audit trail, the source for UI projections,
and the raw material for future analytics/learning. Append-only, never
updated or deleted.

## 5. Temporal mapping

Why Temporal at all: an intake conversation is a **days-long process with
humans in the middle** — waiting on customer replies, waiting on dispatcher
approvals, retrying flaky LLM/API calls. That is exactly the durable-execution
shape. We get crash-safe waits, retries, and full history for free instead of
building a state-machine-plus-job-queue ourselves.

| Concept | Temporal construct |
|---|---|
| Agent run (one intake conversation) | Workflow, ID `run-{runID}` |
| LLM call, tool execution, DB projection write | Activities |
| Inbound customer message | Signal `inbound_message` |
| Human decision on an action | Signal `decision` |
| UI reading run state | Postgres projection (not workflow queries) |
| Long conversations | `ContinueAsNew` past a history-size threshold |

The agent loop, as workflow pseudocode:

```
for run is open:
    await signal (inbound message)            // durable wait, days OK
    resp   := activity: llm.Complete(context) // retries handled by Temporal
    for each tool call in resp:
        action := record ActionProposed        // activity: append event
        pd     := policy.Evaluate(action)
        if pd == RequireApproval:
            decision = await signal `decision` // durable wait on human
        else:
            decision = auto-approve
        record DecisionMade
        if approved:
            result := activity: tool.Execute(effectiveInput)
            record ActionExecuted
        feed decision + result back into agent context
    // agent may propose more actions (revise after rejection) or
    // yield back to waiting for the next inbound message
```

Rules this imposes (these go in CLAUDE.md as hard conventions):

- **All non-determinism lives in activities** — LLM calls, DB access, clocks,
  UUIDs, HTTP. Workflow code is pure orchestration.
- **Workflow state is authoritative while a run is live.** Postgres holds
  *projections* written by activities; the UI reads Postgres and writes
  signals. We accept the small projection lag in exchange for cheap reads and
  a UI that doesn't hammer workflow queries.
- Signals carry IDs, not blobs: `decision(actionID, kind, editedInput, reason)`.
- Signal delivery + idempotent projection activities give us effective
  exactly-once recording; projection writes are keyed on event ID.

## 6. LLM abstraction (provider-agnostic)

agentkit defines a minimal chat-with-tools interface; adapters implement it:

```go
type LLM interface {
    Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
}

type CompletionRequest struct {
    Model    string
    System   string
    Messages []Message   // user | assistant | tool_result turns
    Tools    []ToolDef   // name, description, JSON Schema
    MaxTokens int
}

// CompletionResponse: text and/or []ToolCall{ID, Name, Input}, StopReason, Usage
```

Design stance: keep this interface **deliberately small** — the intersection
of Anthropic/OpenAI-style tool-calling chat, not the union of every provider
feature. Provider-specific capabilities (caching hints, thinking budgets) go
in adapter-level options, not the core interface. First adapter: Anthropic.
Second (OpenAI-compatible) follows once the demo works, to keep the
abstraction honest.

## 7. Channel abstraction (app layer)

A Channel is a bidirectional message transport with a customer:

```go
type Channel interface {
    Name() string // "whatsapp", "sms", "simulated"
    Send(ctx context.Context, conversationID string, msg OutboundMessage) error
}
// Inbound: each channel adapter (webhook handler, simulator) normalizes
// incoming messages and signals the conversation's workflow.
```

v1 ships **SimulatedChannel**: a pane in the web UI where you type as the
customer. Inbound goes through the exact same signal path a real webhook
would; outbound renders in the pane. Meta Cloud API / Twilio adapters later
implement the same interface — the agent, workflow, and UI never know the
difference.

## 8. v1 scope: WhatsApp intake demo

One agent (**intake**), one channel (simulated WhatsApp), one workflow type.

**Flow:** a "customer" sends a message → intake run starts (or resumes) → the
agent gathers job details over the conversation, maintaining a structured job
record → every outbound reply and every job mutation is an Action the
dispatcher approves/edits/rejects in the web UI → conversation continues until
the agent proposes `close_job` (also approved) → run completes.

**Intake agent tool set (v1):**

| Tool | What it does |
|---|---|
| `send_message` | Send a reply to the customer (the draft-reply action) |
| `update_job` | Create/update the structured job record (customer, address, issue, urgency) |
| `close_job` | Mark intake complete, end the run |

Policy: `update_job` is auto-approved — it is internal, reversible
record-keeping, and reviewing every patch is noise. `send_message`
(customer-facing) and `close_job` (terminal) require approval. This was the
first turn of "the slider": v1 shipped with all three requiring approval.

**Web UI (minimal, demos the loop):** a React SPA in `web/` — Vite,
TanStack Router + TanStack Query, Tailwind. One dispatcher view:
conversation list, message thread, pending-action cards with
Approve / Edit / Reject(+reason), and the job record — plus the customer
simulator pane. It talks only to the Go server's JSON API (the API is the
contract), with TanStack Query polling for liveness (SSE can replace
polling later if needed). The server can embed the built assets later if we
want single-binary deploys.

**Explicit non-goals for v1** (each needs its own design doc when it comes):
real WhatsApp adapters, auto-approval policies, scheduling/routing agents,
multi-agent coordination, authn/authz, billing, learned confidence. Two cheap
future-proofs we *do* take now: `org_id` on every table (retrofitting
multi-tenancy is brutal) and ULIDs for all IDs.

## 9. Repo layout

Single Go module. `agentkit` is a top-level package tree with an import-lint
rule (`agentkit` must not import `app`) enforced in CI later, by review now.

```
dispatch/
├── CLAUDE.md
├── docs/design/           # numbered design docs; this is 000
├── agentkit/              # foundational library (extraction candidate)
│   ├── action.go          # Action, Decision, state machine
│   ├── run.go             # Run, agent definition
│   ├── tool.go            # Tool interface, registry
│   ├── policy.go          # Policy interface, static table impl
│   ├── event.go           # event types, append-only log
│   ├── llm/               # LLM interface
│   │   └── anthropic/     # first adapter
│   ├── temporalkit/       # agent-loop workflow pattern, signal/activity helpers
│   └── store/             # storage interfaces + postgres impl
├── app/
│   ├── domain/            # Job, Customer, Conversation, Message
│   ├── agents/intake/     # prompt, tool implementations, policy config
│   ├── channel/           # Channel iface, simulated/, (whatsapp/ later)
│   ├── server/            # HTTP JSON API
│   └── worker/            # Temporal worker registration
├── cmd/
│   ├── server/            # API server binary
│   └── worker/            # temporal worker binary
├── web/                   # React SPA: Vite, TanStack Router/Query, Tailwind
├── migrations/
└── docker-compose.yml     # postgres + temporal dev server
```

## 10. Decisions & tradeoffs log

| Decision | Alternative | Why |
|---|---|---|
| Temporal for agent runs | State machine + job queue in Postgres | Durable multi-day waits and retries are the hard part; Temporal makes them primitives. Cost: operational dependency, determinism discipline. |
| UI reads Postgres projections | Query workflows directly | Cheap reads, history survives run completion, UI decoupled from workflow internals. Cost: projection lag (fine — humans are the slow part). |
| Signals for human decisions | Temporal Updates | Signals are simpler and battle-tested; we don't need the synchronous response. Revisit if we want decision validation to reject bad decisions inline. |
| agentkit as package, not repo | Separate module/repo now | Primitives will churn during v1; the import rule preserves the boundary, extraction stays cheap. |
| Provider-agnostic LLM interface now | Anthropic-only, abstract later | User decision; kept honest by keeping the interface to the tool-calling-chat intersection. |
| React SPA (Vite + TanStack Router/Query + Tailwind) | Server-rendered Go templates + htmx | User decision. Costs a frontend toolchain, buys the UI we'd end up with anyway; the JSON API is the contract, and the server can embed built assets for single-binary deploys. |
| Simulated channel first | Twilio/Meta from day one | Demo needs the approval loop, not webhook plumbing; Channel interface keeps the swap contained. |

### Trigger to extract & publish `agentkit`

The decision above defers extraction; this is the concrete signal to revisit
it, so it's an action and not a vibe. Extract `agentkit` into its own
repo/published module when **either**:

- **A second consumer appears** — another agent business, or a second internal
  app on the same primitives. This is the primary trigger: the second
  consumer's disagreement with the first is the design input that tells us
  what the stable public surface actually is. With one consumer, any public
  API is a guess (same reason we keep `Job` typed rather than generic — don't
  stabilize an abstraction before the second instance forces its shape).
- **Or** the core primitives (`Action`, `Policy`, `Tool`, the event log,
  `temporalkit`) **stop churning** for a real stretch *and* we specifically
  want the library public/reusable.

Two distinctions to keep straight when the trigger fires:

- *Separate module* (mechanical, low-cost, reversible) vs. *published
  publicly* (outward-facing: license, indexing, API-stability expectations).
  We'll almost certainly want the first well before the second.
- Until the trigger fires, the boundary is held by an **import lint**
  (`agentkit` must not import `app`) rather than a split. An optional
  reversible halfway step is promoting `agentkit` to its own module *in this
  repo* via `go.work` (local `replace`, no version tax), which makes a future
  repo-split a `git filter-repo` + push rather than an untangling.

## 11. Open questions (deliberately deferred)

- **Run granularity beyond intake:** is a run per-conversation, per-job, or
  per-agent-task once scheduling agents exist? Likely per-task with a
  coordinating parent — needs its own doc.
- **Decision timeouts:** what happens when a dispatcher doesn't decide for
  hours? (Reminders, escalation, agent sends a holding message?) v1: the run
  just waits.
- **Concurrent actions:** v1 processes one action at a time per run. Batched
  proposals ("here are 3 things I want to do") change the UI and the loop.
- **Learning from edits:** original-vs-edited pairs are captured from day one;
  how they feed prompts/evals/fine-tuning is future work.
