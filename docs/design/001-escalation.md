# Design 001: Escalation — Emergency Handling & Human Attention

**Status:** Accepted — v-next implemented (2026-07-07)
**Date:** 2026-07-07
**Scope:** How the intake agent flags a conversation for urgent human
attention (emergencies today; low-confidence and stuck runs later), and how
that attention reaches a dispatcher fast — without breaking the Action/Policy
model from `000-foundation.md`.

Builds on `000-foundation.md`. Does not change any decision made there; it
adds a new tool, a new event type, and a projected conversation state, all on
top of existing primitives.

---

## 1. Problem

The v1 intake flow funnels everything through one gate: gather name +
address + issue + urgency, send a recap, then `close_job`. That is correct for
a leaking tap. It is wrong for a gas leak.

Two concrete failures today:

1. **No way to summon a human mid-intake.** The system prompt tells the agent,
   on danger (gas smell, sparking, major flooding), to set
   `urgency=emergency`, give a safety instruction, and "finish intake
   quickly." But there is no mechanism that actually makes a human *engage
   sooner*. An emergency conversation sits in the same undifferentiated list
   as a routine one; the dispatcher has no signal to look at it first.
2. **The safety message waits in the approval queue.** `send_message` requires
   approval (correctly, in general). So the one message that must go out in
   seconds — "turn off your gas at the meter and leave the house" — is blocked
   on the same human review latency as "Thanks, what's your address?" In an
   emergency, review *latency* is the hazard, not review itself.

We need a way for the agent to say **"a human needs to engage with this run,
now"** — distinct from proposing another customer-facing action.

## 2. What escalation is (and what it is not)

Escalation is **orthogonal to approval**, and keeping them separate is the
whole design.

| | Approval (existing) | Escalation (this doc) |
|---|---|---|
| Question it answers | "Should *this action* execute?" | "Should a human *engage with this run*, now?" |
| Granularity | Per Action | Per Run / conversation |
| Governed by | `Policy.Evaluate` | The agent raising it + a resolution by a human |
| Effect | Gate/execute a tool call | Reprioritize the run + notify the dispatcher |
| Default | `RequireApproval` | Not raised |

This distinction matters for **principle 6** ("HITL is policy, not
architecture; never hardcode 'ask the human'"). Escalation is *not* a hardcoded
HITL gate. It does not decide whether a human is involved — the approval policy
still does that, unchanged. Escalation changes only *how urgently* the human
responds. A reader should not see `escalate` and think we bypassed the policy
layer: the safety `send_message` still flows through `Policy.Evaluate` exactly
as before. Escalation just makes the human answer it in seconds.

## 3. Design

### 3.1 `escalate` — a new app-level tool

Add one domain tool to the intake agent's tool set:

```
escalate(reason: string, category: "emergency" | "stuck" | "other")
```

- **`reason`** — one line for the dispatcher ("Customer reports gas smell in
  kitchen; advised to leave and shut off meter").
- **`category`** — why we're escalating. `emergency` is the v-next driver;
  `stuck` (agent can't make progress) and `other` are placeholders that let
  the same tool absorb future triggers without a schema change.

The agent decides *whether* to escalate from its own judgment — the prompt
says "if something seems unsafe, or you judge a human should step in, escalate,"
with no catalogue of trigger situations and no keyword rules. Rigidly
enumerating what counts as an emergency would fail the long tail of real
conversations; the model is the classifier.

Executing `escalate`:

1. Writes the **attention projection** on the conversation: state → `flagged`,
   with the reason + timestamp (the tool's `Execute` writes it directly, the
   same way `update_job`/`close_job` write their domain tables). The escalate
   *Action itself* is the raised record on the append-only log — see §4.
2. The dispatcher UI surfaces flagged conversations at the top of the list
   with the "needs decision" safety-orange treatment (per the Hi-Vis design
   system — safety orange is reserved for exactly this: something needs a
   human decision now).
3. (Future, §6) fires an out-of-band notification (push/SMS/page).

### 3.2 Policy: `escalate` is `AutoApprove`

You do not ask permission to raise an alarm. `escalate` is added to the intake
policy table as `AutoApprove`, alongside `update_job`. It still creates an
Action record and flows through the pipeline (principle 2 — no side doors), so
the escalation is fully audited: what the agent saw, when it raised, what
reason. It simply isn't gated.

### 3.3 The safety-message latency problem

Escalation reprioritizes and notifies; it does **not** by itself send the
customer's safety instruction — that is still a `send_message` under the
approval policy. We solve the latency two ways, in order of preference:

1. **Notify + prioritize (v-next).** The escalation pages/surfaces the
   conversation so the pending safety `send_message` is approved in seconds,
   not minutes. The human stays in the loop; we attack latency, not oversight.
   This is the honest first step and keeps a human on every customer-facing
   emergency message.
2. **Context-aware auto-approval (future, gated).** Because `Policy.Evaluate`
   already receives `ctx` and the `Action`, a later policy can read the run's
   attention state and auto-approve `send_message` *while escalated* — the
   "slider" from the foundation, applied to emergencies. This is a real safety
   decision (auto-sending customer-facing text) and is deferred to the
   auto-approval design doc, not smuggled in here. Escalation is the
   **input that makes that future policy possible**; it does not itself change
   approval behavior.

Framing it this way keeps the two axes clean: escalation is a signal; whether
that signal *relaxes approval* is a policy question decided later.

### 3.4 Decoupling emergency from `close_job`

Emergencies must not be forced through the full-intake gate. Escalation is
independent of completion:

- The agent can `escalate` mid-conversation, keep talking (deliver the safety
  step, gather only what's critical), and leave the run open for a human.
- A flagged run is **not** auto-closed. Resolution is a human act: the
  dispatcher acknowledges the escalation (§4) once they've engaged — taking
  over the phone, dispatching a truck, etc.
- The `close_job` contract (name + address + issue + urgency + recap) stays as
  the *routine* completion path. An escalated emergency may never hit it; the
  human owns the outcome from the point of escalation.

## 4. Data model & events (append-only, principle 4)

No mutable-state shortcuts. Escalation lives on the append-only log and is
projected to a conversation-level attention state.

**The grain to respect: agent acts are Actions; human acts are events.** The
system already follows this — an agent tool call becomes an Action (proposed →
decided → executed), while a dispatcher's decision on it is a `decision_made`
*event*, not a second "decision action." Escalation maps onto the same grain:

- **Raise = the agent's `escalate` Action.** It already flows through the
  pipeline as `action_proposed` / `decision_made` (auto-approve) /
  `action_executed`, with the `reason` + `category` preserved in the Action's
  input. That *is* the raised record on the log — and, by construction, future
  training data. A separate `escalation_raised` event would only duplicate it
  and break the grain, so we do **not** add one.
- **Acknowledge = a new `escalation_acknowledged` event** — payload
  `{conversation_id, acknowledged_by, note}`. A dispatcher engaging is a
  human-initiated act with no backing Action, exactly like a decision, so it
  gets its own event appended by the acknowledge endpoint.

Projection — the current-view columns the dispatcher UI reads. As in v1 (jobs,
conversation status), the projection is written directly by the code that
causes it (the `escalate` tool on raise; the acknowledge endpoint on ack)
rather than by a separate event-replay worker; the log stays the source of
truth and audit:

```
conversations.attention_state : "none" | "flagged" | "acknowledged"
conversations.attention_reason: text
conversations.escalated_at    : timestamptz
```

A conversation can be flagged → acknowledged → (re-)flagged if a second
emergency arises; the event log is the history, the projected columns are the
current view. Migration adds these columns to `conversations` (nullable /
defaulted, so existing rows are `none`).

## 5. Boundary: why this is app-only (for now)

Per the agentkit test ("would a non-dispatch agent business need this?"),
"flag this run for urgent human attention" is genuinely general — a support
agent, a trading agent, a moderation agent all want it. So there *is* a future
agentkit primitive here: a run-level **attention/priority** field and an
`attention_raised` event on the core event log.

We deliberately **do not** build that yet:

- v-next needs zero agentkit changes. `escalate` is an ordinary `Tool`; the
  Action pipeline, event log, and policy table already carry it. The
  emergency semantics (categories, safety behavior, UI treatment) are all
  domain, so they belong in `app`.
- Lifting attention into agentkit before a second business exists risks
  designing the wrong primitive. When a second agent business needs "urgent
  human attention," we promote the app pattern into agentkit — the same
  extraction discipline the foundation applies to agentkit as a whole.

Stated as a decision so a future reader knows it was intentional, not an
oversight.

## 6. Phasing

**v-next (this doc):**
- `escalate` tool (app), `AutoApprove` policy entry.
- `escalation_raised` / `escalation_acknowledged` events + `conversations`
  attention projection + migration.
- Dispatcher UI: flagged conversations sort to top, safety-orange treatment,
  an "Acknowledge" action; reason shown inline.
- Prompt update: the agent escalates from its own judgment ("if something
  seems unsafe, or a human should step in") — no enumerated triggers, no
  keyword rules — and, when it escalates for safety, also sends the customer
  the key safety step without waiting for full intake.

**Future (own docs / the auto-approval doc):**
- Context-aware policy that auto-approves customer messages while escalated
  (§3.3.2) — a safety decision, gated behind the auto-approval design.
- **Human takeover**: dispatcher takes over the conversation, agent yields
  control and stops proposing until handed back. Bigger UX + loop change.
- **External notification**: push/SMS/phone paging of the on-call dispatcher.
  v-next relies on in-UI surfacing + polling.
- Promotion of attention/priority into agentkit once a second business needs
  it (§5).

## 7. Relationship to foundation open questions

This doc partially answers one of `000-foundation.md` §11's deferred
questions:

- **Decision timeouts** ("what happens when a dispatcher doesn't decide for
  hours — reminders, escalation, holding message?"). Escalation is the
  mechanism a timeout would trigger: a run whose approval has waited too long
  can auto-`escalate(category=stuck)` to raise its own priority. The timeout
  *trigger* is still deferred; escalation gives it somewhere to land. The
  `stuck` category is reserved for exactly this.

## 8. Open questions

- **Auto-acknowledge on first human action?** If a dispatcher approves the
  escalated conversation's safety message, is that an implicit acknowledge, or
  do we require an explicit one? Leaning implicit (engaging *is*
  acknowledging), with an explicit override.
- **De-escalation / false alarms.** If the agent escalates and it turns out
  routine, who clears the flag — the agent (a `resolve`/`de_escalate` tool) or
  only the dispatcher? Leaning dispatcher-only for v-next (a human decided it
  wasn't urgent), agent-driven de-escalation later.
- **One flag or a queue?** v-next uses a single `attention_state` per
  conversation. If multiple distinct emergencies stack, do we need an
  escalation list per run? Deferred until it's real.
- **SLA / priority tiers.** `emergency` vs `stuck` probably warrant different
  surfacing and (later) different notification urgency. v-next treats both as
  "flagged"; tiering follows the external-notification work.
