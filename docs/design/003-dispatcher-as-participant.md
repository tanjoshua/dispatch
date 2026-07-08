# Design 003: The Dispatcher Is Always a Participant

**Status:** Accepted — implemented
**Date:** 2026-07-08
**Scope:** Make the human dispatcher a first-class participant in every
conversation — able to message the customer directly at any time — and remove
the notion of an "agent's turn" that a human "takes over." The agent never owns
the conversation; it reads the full shared context and acts when it judges it
should.

Builds on `000-foundation.md`. It **changes** the interaction model shipped in
the current code (per-draft *Dismiss* framed as the agent "standing down" /
"handling the conversation for now") and **supersedes** the *Human takeover*
future item named in `001-escalation.md` §6. Both changes are called out
explicitly in §7.

---

## 1. Problem

The v1 loop gives the customer and the agent a voice, but not the dispatcher.
The only way a message reaches the customer is the agent's `send_message`
Action, which the dispatcher may approve / edit / revise / dismiss. The
dispatcher can shape or veto the agent's words, but cannot **say their own** —
there is no path for "let me just handle this one myself."

Worse, the stopgap we shipped for that need — *Dismiss* — modeled it as a
**mode**. Dismissing a draft made the agent "stand down and hand the
conversation over for now… wait for the customer's next message." That framing
is the thing this doc rejects. It recreates, in miniature, the
`agent's turn` ↔ `human takeover` control-handoff that `001-escalation.md` §6
had pencilled in as a big future feature. Control handoff is the wrong model:

- It forces a false question — *whose turn is it?* — onto what is really one
  shared conversation with the customer.
- It creates modes to enter, exit, and get stuck in (agent stood down; who
  hands it back? when?).
- It fights the product thesis. The dispatcher reviews the agent the way a
  developer reviews a coding agent — and a developer can always just *type the
  code themselves* without "taking over" from the agent. The agent doesn't lose
  the wheel; there is no wheel to lose.

## 2. The model: one conversation, two authors, shared context

There is **one** conversation with the customer. Two parties can author
messages into it:

- the **agent**, via `send_message` Actions (proposed → reviewed → sent), and
- the **dispatcher**, directly, at any time.

Neither has "the turn." The dispatcher can send a message while a draft is
pending, mid-intake, after an escalation — whenever. The agent is not gated on,
or by, the human; it simply **reads the whole shared context** — customer
messages, its own sent messages, *and* messages the dispatcher sent directly —
and decides what, if anything, to do.

Concretely, three principles:

1. **The dispatcher can always reply.** A first-class dispatcher message goes to
   the customer through the same outbound path the agent uses (`Sender` →
   adapter), and is recorded with `author = dispatcher`.
2. **The agent always sees everything.** A dispatcher message is injected into
   the agent's context, labeled as coming from the human operator, so the
   agent's next turn is fully informed. No hidden state; no "the agent doesn't
   know what you said."
3. **There is no takeover mode.** Nothing flips the agent off. Whether the agent
   speaks is a matter of *what it sees and its judgment*, governed — as always —
   by the approval policy, not by a control flag. (Principle 6 of `CLAUDE.md`:
   HITL is policy, not architecture. A takeover mode would have been
   architecture.)

### 2.1 When does the agent act?

Agent turns stay **customer-driven**: the agent runs a turn when the *customer*
sends a message, exactly as today. A dispatcher message updates the shared
context but does **not** by itself provoke an agent turn — the agent does not
talk over a human who is actively handling the conversation. When the customer
next replies, the agent runs with the dispatcher's messages already in context
and uses judgment: continue intake, stay quiet and just keep the job record
current, or defer to what the dispatcher already handled.

> **Decision (recommended, and what we ship): dispatcher messages are context,
> not a trigger.** The alternative — waking the agent on every dispatcher
> message so it can chime in — invites the agent to step on the human and adds
> no capability the customer-driven model lacks (the agent catches up the moment
> the customer speaks). We keep the trigger customer-only and let *judgment +
> policy*, not a new trigger, decide whether the agent contributes. If a real
> need for agent-initiated turns appears (e.g. proactive follow-ups), it gets
> its own trigger design, not a reflexive answer to dispatcher messages.

This keeps the loop's shape intact (`000-foundation.md` §5): the durable wait is
still "await the next customer message," now widened to also absorb dispatcher
messages into context.

### 2.2 A dispatcher reply supersedes a pending draft

If the agent has a `send_message` draft awaiting review and the dispatcher, in
that moment, sends their own reply instead of deciding on the draft, the human
has answered the customer. The pending draft is **superseded**: it is resolved
out of `pending_approval` (never sent), the dispatcher's message is delivered
and injected into context, and the current agent turn ends. The agent re-engages
— informed — on the customer's next message. This is the honest "I've got this
one" gesture, and it needs no mode: it is just *the dispatcher sending a
message* while a draft happened to be open.

## 3. What "no takeover" does to *Dismiss*

*Dismiss* survives, but shed of the mode framing. It now means exactly one
thing: **discard this draft; don't send it.** It is the escape hatch for "not
this message" when the dispatcher has nothing to send themselves and no revision
to ask for. After a dismiss the agent does not immediately re-draft the same
turn (that would ignore the human); it waits for the customer's next message,
same as after any completed turn. The agent-facing feedback drops the "handling
the conversation for now / taking over" language and simply states the draft was
not sent.

The distinction that matters:

| Gesture | Customer receives | Pending draft | Agent-facing meaning |
|---|---|---|---|
| Approve / Edit | the (maybe edited) draft | sent | your message went out |
| Revise (reject + reason) | nothing yet | withdrawn | rewrite it now, here's why |
| Dismiss | nothing | withdrawn | not this; wait for the customer |
| **Dispatcher reply** | the dispatcher's words | superseded | the human answered directly; here's what they said |

## 4. Data model & events (append-only, principle 4)

**Message authorship.** `messages` gains an `author` column:
`customer | agent | dispatcher`. `direction` stays (inbound = customer;
outbound = agent or dispatcher) but `author` is the richer field the UI and the
agent context key on. Migration `0004` adds the column and backfills existing
rows (inbound → `customer`, outbound → `agent`, since every prior outbound was
an agent send).

**Dispatcher message = a human act, so it is an event, not an Action.** This
follows the grain `001-escalation.md` §4 set: agent acts are Actions; human acts
are events. A dispatcher reply has no backing agent proposal, so the reply
endpoint appends a `dispatcher_message` event (payload
`{conversation_id, message_id, sent_by}`) to the run's log alongside persisting
and delivering the outbound message. The message row itself is the projection
the UI reads.

Superseding a pending draft *is* an Action state change, so it flows through the
existing pipeline: the workflow records a `decision` on the draft (moving it out
of `pending_approval`) exactly as a dismiss does — no side door (principle 2).

## 5. Control flow (Temporal)

A third signal joins `inbound_message` and `decision`:

- **`dispatcher_message`** — payload `{message_id, text}`. Carries the human
  reply's id and text into the run for context (the blob — the message body —
  is already persisted and delivered by the endpoint before signaling, mirroring
  how inbound persists-then-signals; the signal stays small per `000` §5).

The loop gains reactivity to it at both durable waits:

- **Top-level wait** (between customer turns): selects over `inbound_message`
  and `dispatcher_message`. Both are drained into context; an agent turn runs
  only if at least one *customer* message arrived.
- **Draft-review wait** (a pending Action): selects over `decision` and
  `dispatcher_message`. A decision resolves the draft as before. A dispatcher
  message supersedes the pending draft (records its decision), injects the human
  note into context, and aborts the current turn back to the top-level wait.

All non-determinism stays in activities (delivery, persistence, decision
recording); the workflow only orchestrates the selects (principle 5). The
`dispatcher_message` endpoint delivers synchronously through `Sender` (so a real
transport's failure surfaces to the dispatcher immediately) and only signals a
**live** run; a reply on a conversation whose run has finished is delivered and
recorded but has no agent to inform, which is correct.

## 6. Boundary (agentkit vs app)

The primitive "a human participant messages into a run's context" is general —
so the `dispatcher_message` signal and the human-note context injection live in
`temporalkit` (agentkit), keyed on no dispatch domain concept. The reply
*endpoint*, the `author` values, the `dispatcher_message` *event type*, and the
UI are `app`. As with escalation (`001` §5) and channels (`002` §8), we do not
lift a speculative "participant/roles" model into agentkit before a second
business proves its shape; agentkit gains only the minimal signal + context
seam it already needs to carry a non-customer message.

## 7. Changes to earlier decisions, stated explicitly

Per the numbered-doc convention:

1. **Dismiss semantics (current code, commits reframing "Reject" → "Revise" and
   adding per-draft Dismiss).** *Dismiss* no longer means the agent "stands
   down / takes over"; it means "discard this draft." The agent-facing dismiss
   feedback loses its takeover wording (§3). The mechanism (a `decision` that
   withdraws the draft and ends the turn) is unchanged.
2. **`001-escalation.md` §6 "Human takeover" (future).** Removed as a planned
   feature and replaced by this model. There is no takeover because there is no
   turn to take over: the dispatcher is always a participant. Escalation still
   does its job — reprioritize + notify — and "a human is engaging" is now
   expressed by the dispatcher simply *messaging*, not by flipping the agent
   off.

Everything else in `000`/`001`/`002` stands. This doc adds one column, one
event type, one signal, one endpoint, and the two selects above.

## 8. Open questions

- **Agent-initiated turns.** Deferred (§2.1). If proactive agent behavior is
  wanted, it needs its own trigger, not a reaction to dispatcher messages.
- **Reply on a finished run.** We deliver + record but don't spin up an agent.
  If a later customer message starts a fresh run, that run begins without prior
  context (a pre-existing v1 limitation — runs don't replay full history). When
  full-history rehydration lands, dispatcher messages come along for free via
  `author`.
- **Attribution of dispatcher identity.** `sent_by` is a free string
  ("dispatcher") until auth/users land (`000` §8 non-goal); it graduates to a
  real user id then.
- **Does a dispatcher reply auto-acknowledge an escalation?** Plausibly
  (engaging *is* acknowledging — the leaning in `001` §8). Left as-is for now:
  the reply and the acknowledge are separate acts; wiring "reply implies ack"
  is a small follow-up once we see the ergonomics.
</content>
</invoke>
