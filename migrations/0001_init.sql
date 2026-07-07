-- agentkit tables: runs, actions, events.
-- Business-agnostic; the app's domain tables live further down.

CREATE TABLE runs (
    id         TEXT PRIMARY KEY,
    org_id     TEXT NOT NULL,
    agent      TEXT NOT NULL,
    status     TEXT NOT NULL, -- running | completed | failed
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE actions (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    run_id          TEXT NOT NULL REFERENCES runs (id),
    tool_call_id    TEXT NOT NULL, -- LLM tool-call ID; idempotency key for proposals
    tool            TEXT NOT NULL,
    input           JSONB NOT NULL,
    edited_input    JSONB,          -- set when decision = approve_with_edits; input is never overwritten
    state           TEXT NOT NULL,  -- proposed | pending_approval | approved | approved_with_edits | rejected | executing | completed | failed
    decision_kind   TEXT,           -- approve | approve_with_edits | reject
    decided_by      TEXT,           -- user ID, or "policy:auto"
    decision_reason TEXT,
    result          JSONB,
    error           TEXT,
    proposed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at      TIMESTAMPTZ,
    executed_at     TIMESTAMPTZ,
    UNIQUE (run_id, tool_call_id)
);

CREATE INDEX actions_run_id_idx ON actions (run_id);
CREATE INDEX actions_state_idx ON actions (org_id, state);

-- Append-only. Never UPDATE or DELETE rows here.
CREATE TABLE events (
    id         TEXT PRIMARY KEY,
    org_id     TEXT NOT NULL,
    run_id     TEXT NOT NULL,
    type       TEXT NOT NULL, -- message_received | action_proposed | decision_made | action_executed | run_completed | ...
    payload    JSONB NOT NULL DEFAULT '{}',
    dedupe_key TEXT NOT NULL, -- idempotency key for at-most-once appends under activity retries
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, dedupe_key)
);

CREATE INDEX events_run_id_idx ON events (run_id, created_at);

-- app domain tables

CREATE TABLE customers (
    id         TEXT PRIMARY KEY,
    org_id     TEXT NOT NULL,
    phone      TEXT NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, phone)
);

CREATE TABLE conversations (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL,
    customer_id TEXT NOT NULL REFERENCES customers (id),
    channel     TEXT NOT NULL, -- "simulated" | "whatsapp" later
    run_id      TEXT REFERENCES runs (id),
    status      TEXT NOT NULL DEFAULT 'open', -- open | closed
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX conversations_org_idx ON conversations (org_id, updated_at DESC);

CREATE TABLE messages (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations (id),
    direction       TEXT NOT NULL, -- inbound (from customer) | outbound (to customer)
    body            TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX messages_conversation_idx ON messages (conversation_id, created_at);

CREATE TABLE jobs (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations (id) UNIQUE,
    customer_name   TEXT NOT NULL DEFAULT '',
    phone           TEXT NOT NULL DEFAULT '',
    address         TEXT NOT NULL DEFAULT '',
    issue           TEXT NOT NULL DEFAULT '',
    urgency         TEXT NOT NULL DEFAULT '', -- low | normal | high | emergency
    status          TEXT NOT NULL DEFAULT 'intake', -- intake | intake_complete
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
