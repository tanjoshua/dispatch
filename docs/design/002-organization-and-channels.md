# Design 002: Organization & Channel Connections

**Status:** Accepted — first slice of the organization/configuration foundation
**Date:** 2026-07-08
**Scope:** Make `Organization` a real first-class aggregate, and make a
`Channel` a *connection an org owns* rather than a hardcoded adapter. Ships a
**dev channel** as the first connection kind. Playbooks (org-tailored flows)
and CRM enrichment are named here but deferred to their own docs.

Builds on `000-foundation.md`. It **activates** the `org_id`-everywhere
future-proof that doc took (§8) and it **changes** two things that doc
specified for v1 — the `Channel` interface and how inbound resolves the org.
Both changes are called out explicitly in §6 and §7.

---

## 1. The leap this doc starts

Today Dispatch is a well-built *single-tenant demo wearing multi-tenant
clothing*. `org_id` is on every table (good), but it is only ever the string
`s.OrgID` — a server-global constant. The agent that runs is `s.AgentName` —
another global. The "channel" is the literal string `"simulated"` written onto
the conversation. Inbound arrives at one hardcoded endpoint that reads those
globals.

That is correct for a demo and wrong for a product. The product is **dispatch
software other businesses run**: each business (an *org*) connects its own
intake channels, tailors what the agent gathers and does (its *playbook*),
and manages its own customers. This doc takes the first, load-bearing step of
that leap — and only the first.

The guiding principle, decided in conversation and reaffirmed here:

> **Variation across orgs happens through narrow, declarative configuration
> that selects and parameterizes our code-defined capabilities — never a
> general no-code workflow engine.** Code owns the verbs (tools, adapters, the
> agent loop). Config owns which verbs are active, with what schema, under what
> policy, in whose voice. A genuinely new capability is new code surfaced as a
> config toggle, not a canvas where orgs wire logic.

## 2. Where this heads (context, not scope)

The org is the tenant root that will eventually own:

| The org owns | Realized as | Status |
|---|---|---|
| Identity & settings (name, org-level settings) | `Organization` aggregate | **This doc (a)** |
| Connected intake channels (dev, later WhatsApp/SMS/email) | `Channel` *connections* + per-kind adapters | **This doc (b)** |
| Its customers & their history (CRM) | `Customer` promoted to a real aggregate | Deferred (own doc) |
| Its tailored flow (what to gather, which tools, completion, dispatch) | `Playbook` config assembling code capabilities | Deferred (own doc) |
| Its autonomy setting (the ask→auto slider) | Per-org/per-playbook `Policy` config | Deferred (auto-approval doc) |
| Its team (dispatchers, roles) | Users/members + authn | Deferred (non-goal, per 000 §8) |

This doc implements only the first two rows. The rest is drawn so the reader
sees the shape the aggregate must support — and so the two things we *do* build
are cut to fit it.

## 3. The central insight: org identity rides on the channel

The reason (a) and (b) are one slice, not two: **the channel connection is
what carries org identity into every inbound message.**

Today inbound resolves the org from a server global:

```
inbound → s.OrgID (constant) → customer → conversation → run
```

That is the single-tenant assumption baked into the request path. The fix is
not "add a tenant router" — it is to notice that a real inbound message always
arrives *somewhere*: a specific WhatsApp business number, a specific email
inbox, a specific dev pane. That "somewhere" is a **channel connection**, and a
connection belongs to exactly one org. So:

```
inbound on a channel connection → connection.OrgID → customer → conversation → run
```

Once org is resolved *from the connection the message arrived on*, the org
aggregate is real, the global constant is gone, and multi-org becomes "there is
more than one connection" — with **no change to the request path**. That is why
making the org first-class and making channels connections are the same move.

## 4. (a) The `Organization` aggregate

Promote org from a constant to a row and a resolved value.

**Model** (`app/domain`):

```go
type Organization struct {
    ID        string          // ULID, the tenant root every table already keys on
    Name      string
    Settings  json.RawMessage // open bag for small org-level settings; typed fields graduate out of it as they earn it
    CreatedAt time.Time
}
```

**Migration:** add an `organizations` table; seed one row (`org_dev`) so
existing data and the dev environment keep working unchanged. Every existing
table already carries `org_id`; this gives that column a real parent.

**Resolution, not routing.** We do *not* build tenant routing, subdomains, or
auth here (still a 000 §8 non-goal). We change one thing: code stops reading a
global `s.OrgID` and starts reading the org **off the channel connection** an
inbound message arrived on (§3), and off the conversation/customer for
everything downstream. In practice there is still one org in dev — but it is
resolved, not assumed. The seam that later carries a second org is now in place.

**What stays out:** org self-service signup, billing, per-org users/roles.
Onboarding is **white-glove** for now — an org and its connections are seeded
records (a small admin script / SQL), not a signup flow. This keeps config
code-adjacent while we learn its shape, exactly the discipline 000 applies to
agentkit extraction.

## 5. (b) `Channel` as a connection

Split today's single conflated "channel" into two things with different
lifetimes:

- **Channel *kind* = code.** How to send and receive on a transport, and what
  credentials/config that transport needs. `dev`, later `whatsapp`, `sms`,
  `email`. Ships as an adapter implementing an interface; registered once.
- **Channel *connection* = data.** One org's configured use of a kind: *this
  org's* WhatsApp business number and credentials, or *this* dev pane. A row.

**Model** (`app/domain`):

```go
type ChannelConnection struct {
    ID        string          // ULID
    OrgID     string          // the org this connection belongs to — carries tenancy (§3)
    Kind      string          // "dev" | "whatsapp" | ... — selects the adapter
    Address   string          // the business-side identity inbound is addressed to
                              //   (WhatsApp business number; a dev token). Lookup key for inbound.
    Config    json.RawMessage // per-kind config/credentials (empty for dev)
    Status    string          // "active" | "disabled"
    CreatedAt time.Time
}
```

A `Conversation` now references the **connection** it belongs to
(`channel_id`), replacing the bare `channel string` name. Its org is the
connection's org.

**Adapter interface + registry.** Today `channel.Channel` is a single injected
instance with `Send(ctx, conversationID, msg)`. That cannot serve multiple
connections of different kinds. It becomes an adapter keyed by kind, plus two
small services that own the shared path:

```go
// One per kind, registered at startup. Thin: transport only.
type Adapter interface {
    Kind() string
    Deliver(ctx context.Context, conn ChannelConnection, to string, msg OutboundMessage) error
}

// Shared outbound path. Resolves the conversation's connection, picks the
// adapter by kind, delivers. The send_message tool holds THIS, not an adapter.
type Sender interface {
    Send(ctx context.Context, conversationID string, msg OutboundMessage) error
}

// Shared inbound path. Every adapter's transport edge calls Receive; it does
// what handleSimulateInbound does today, but resolves org from the connection.
type Router interface {
    Receive(ctx context.Context, conn ChannelConnection, from Sender, text string) (RouteResult, error)
}
```

`Sender` and `Router` are the **production path**; per-kind adapters are thin
transport edges. This is the whole point of the dev channel (§6): the code that
matters runs identically in dev and prod.

## 6. The dev channel — first connection, maximum shared code

The first connection kind is `dev`: a **local-development** channel where a
developer types as the customer (today's simulator pane). The design goal the
user set is precise — *the dev channel must exercise as much of the production
path as possible*, so that "works in local dev" means "the production code
works," not "a parallel demo code path works."

It achieves that by being a genuine connection, not a bypass:

- **Inbound:** the dev channel is a thin HTTP endpoint (`POST /api/dev/inbound`,
  the evolution of `/api/simulate/inbound`) that resolves the dev
  `ChannelConnection` and calls the shared `Router.Receive`. A future WhatsApp
  webhook is a *different* thin endpoint calling the *same* `Router.Receive`.
  The get-or-create customer, open/create conversation, run start,
  message+event append, and signal-with-start logic move out of the endpoint
  and into `Router` — shared verbatim.
- **Outbound:** the dev adapter's `Deliver` writes the outbound `Message` row
  that the UI pane renders — the only kind-specific behavior. The WhatsApp
  adapter's `Deliver` will instead call the Meta API. Everything upstream (the
  `send_message` tool → `Sender` → connection resolution) is identical.
- **The only differences from a prod channel are at the two transport edges**
  (how a byte arrives, how a byte leaves). The agent loop, Action pipeline,
  policy, projections, and UI never know the difference — exactly the property
  000 §7 promised and this makes real.

## 7. Changes to 000, stated explicitly

Per the numbered-doc convention (a later doc that changes an earlier decision
says so):

1. **`Channel` interface (000 §7).** Was a single injected instance with
   `Send(ctx, conversationID, msg)`. Becomes a per-kind `Adapter` plus shared
   `Sender`/`Router` services (§5). Reason: one instance cannot serve multiple
   connections/kinds; the connection is now data.
2. **Org resolution.** Was a server-global `s.OrgID` read on every request.
   Becomes resolved from the channel connection inbound arrived on, and from the
   conversation/customer downstream (§3–§4). Reason: tenancy must ride on the
   message, not the process.

Everything else in 000 stands. This doc adds tables and services; it does not
touch agentkit, the Action pipeline, the event log, or the workflow.

## 8. Boundary: still app-only

Per the agentkit test ("would a non-dispatch agent business need this?"):
`Organization`, `ChannelConnection`, and the dispatch adapters are **app**.
A generic agent business might want *tenancy* and *transports* too, but the
shapes here (dispatch orgs, WhatsApp-style channels) are domain, and — like
escalation in 001 — we do not lift a speculative primitive into agentkit before
a second business proves its shape. agentkit stays untouched by this doc.

## 9. Migration & compatibility

- Add `organizations` and `channel_connections` tables; add `channel_id` to
  `conversations` (nullable, backfilled to a seeded dev connection).
- Seed one org (`org_dev`) and one `dev` channel connection for it, so existing
  data and every `go run` keep working with no manual steps.
- Rename `/api/simulate/inbound` → `/api/dev/inbound` (keep the old path as an
  alias for one release so the web client and any scripts don't break).
- `s.OrgID` / `s.AgentName` globals are removed from the request path; the
  playbook/agent a run uses is still the single seeded agent for now (see §10).

## 10. Deferred to the next docs (named so the seams are intentional)

- **Playbook (org-tailored flow).** *Which* agent/playbook a run uses is still
  `s.AgentName` today. The channel connection (or the org) is where playbook
  **selection** will attach — a connection routes inbound to a playbook. This
  doc deliberately leaves `AgentName` as the single seeded value and reserves
  the binding point; the playbook model (tools + intake schema + policy +
  prompt + dispatch behavior) is its own doc.
- **CRM.** `Customer` stays thin (phone + name) here; promoting it to a real
  aggregate with history is its own doc. Resolving org-from-connection is the
  prerequisite that lets a customer be scoped and de-duplicated correctly per
  org.
- **Per-org policy / the slider.** Policy is still the static per-tool table.
  Making it per-org config is the auto-approval doc; this doc gives it an org to
  hang on.

## 11. Open questions

- **Channel addressing.** Inbound looks up the connection by `(kind, address)`.
  For WhatsApp the business number is the natural address; is a single `address`
  string enough, or do some kinds need a compound key (e.g. email: inbox +
  domain)? `dev` uses a connection id / fixed token, so the question is real
  only when the second kind lands.
- **One playbook per connection, or per conversation?** Likely the connection
  selects a default playbook and a conversation can be re-routed later. Decided
  in the playbook doc.
- **How much of `Organization.Settings` should be typed now?** Leaning: start
  with an open bag, graduate a field to a typed column the moment two orgs (or
  the code) actually depend on it — same typed-core-plus-bag discipline we chose
  for the job schema.
- **Multi-org resolution in one process.** With org-from-connection, a single
  server can host many orgs with no routing change. When do we actually need
  per-org isolation guarantees (connection pools, rate limits) beyond a shared
  `org_id` filter? Deferred until a second org is real.
```
