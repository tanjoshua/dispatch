-- Residential-first conversation correctness: exact identity routing,
-- versioned context, ordered events/outbox, and versioned action commands.

ALTER TABLE customers ADD COLUMN version BIGINT NOT NULL DEFAULT 1;

ALTER TABLE playbooks ADD COLUMN version BIGINT NOT NULL DEFAULT 1;

ALTER TABLE cases
    ADD COLUMN version BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN summary TEXT NOT NULL DEFAULT '';

ALTER TABLE conversations
    ADD COLUMN contact_identity_id TEXT REFERENCES contact_identities (id),
    ADD COLUMN event_seq BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN context_revision BIGINT NOT NULL DEFAULT 0;

-- Backfill the exact identity used by each existing thread. Older data only
-- knew customer + channel kind, so choose deterministically for migration;
-- all new inbound traffic supplies the exact identity explicitly.
UPDATE conversations conv
SET contact_identity_id = (
    SELECT ci.id
    FROM contact_identities ci
    JOIN channel_connections cc ON cc.id = conv.channel_id
    WHERE ci.org_id = conv.org_id
      AND ci.customer_id = conv.customer_id
      AND ci.channel_kind = cc.kind
    ORDER BY ci.created_at, ci.id
    LIMIT 1
);

ALTER TABLE conversations ALTER COLUMN contact_identity_id SET NOT NULL;
DROP INDEX conversations_customer_channel_key;
CREATE UNIQUE INDEX conversations_channel_identity_key
    ON conversations (org_id, channel_id, contact_identity_id);

ALTER TABLE messages
    ADD COLUMN event_seq BIGINT,
    ADD COLUMN delivery_state TEXT NOT NULL DEFAULT 'sent',
    ADD COLUMN provider_delivery_id TEXT,
    ADD COLUMN delivery_error TEXT;

ALTER TABLE messages ADD CONSTRAINT messages_delivery_state_check
    CHECK (delivery_state IN ('queued', 'sending', 'sent', 'failed', 'unknown'));

DROP INDEX messages_provider_message_id_key;
CREATE UNIQUE INDEX messages_provider_message_id_key
    ON messages (org_id, conversation_id, provider_message_id)
    WHERE provider_message_id IS NOT NULL;
CREATE UNIQUE INDEX messages_conversation_event_seq_key
    ON messages (conversation_id, event_seq) WHERE event_seq IS NOT NULL;

CREATE TABLE conversation_events (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations (id),
    seq             BIGINT NOT NULL,
    type            TEXT NOT NULL,
    message_id      TEXT REFERENCES messages (id),
    run_id          TEXT REFERENCES runs (id),
    case_id         TEXT REFERENCES cases (id),
    source_message_ids TEXT[] NOT NULL DEFAULT '{}',
    payload         JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, seq)
);
CREATE INDEX conversation_events_cursor_idx
    ON conversation_events (conversation_id, seq);

CREATE TABLE outbox (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations (id),
    kind            TEXT NOT NULL, -- workflow_wakeup | outbound_delivery
    dedupe_key      TEXT NOT NULL,
    payload         JSONB NOT NULL DEFAULT '{}',
    state           TEXT NOT NULL DEFAULT 'pending',
    attempts        INT NOT NULL DEFAULT 0,
    available_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    UNIQUE (org_id, kind, dedupe_key)
);
CREATE INDEX outbox_pending_idx ON outbox (available_at, created_at)
    WHERE state = 'pending';

ALTER TABLE actions
    ADD COLUMN version BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN context_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN dependency_versions JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN responds_through_event_seq BIGINT,
    ADD COLUMN superseded_at TIMESTAMPTZ;

CREATE UNIQUE INDEX one_current_reply_draft_per_conversation
    ON actions ((dependency_versions->>'conversation_id'))
    WHERE tool = 'propose_response' AND state = 'pending_approval';

CREATE TABLE action_commands (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    action_id       TEXT NOT NULL REFERENCES actions (id),
    expected_version BIGINT NOT NULL,
    expected_context_revision BIGINT NOT NULL,
    kind            TEXT NOT NULL,
    actor_id        TEXT NOT NULL,
    request         JSONB NOT NULL,
    result          JSONB NOT NULL,
    http_status     INT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, id)
);

CREATE TABLE context_snapshots (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations (id),
    run_id          TEXT NOT NULL REFERENCES runs (id),
    context_revision BIGINT NOT NULL,
    event_from_seq  BIGINT NOT NULL,
    event_to_seq    BIGINT NOT NULL,
    triggering_message_ids TEXT[] NOT NULL,
    dependency_versions JSONB NOT NULL,
    context         JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE model_turns (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations (id),
    run_id          TEXT NOT NULL REFERENCES runs (id),
    context_snapshot_id TEXT NOT NULL REFERENCES context_snapshots (id),
    prompt_version  TEXT NOT NULL,
    request         JSONB NOT NULL,
    response        JSONB,
    usage           JSONB NOT NULL DEFAULT '{}',
    disposition     TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Legacy drafts were generated without a context revision and are unsafe to
-- release after this migration. Keep them as audit history.
UPDATE actions
SET state = 'superseded', decision_kind = 'supersede',
    decision_reason = 'superseded_by_migration', version = version + 1,
    decided_at = COALESCE(decided_at, now()), superseded_at = now()
WHERE state = 'pending_approval';
