-- Organizations currently have one code-owned agent pack and one editable
-- behavior record. Keep playbooks as the runtime/audit seam while constraining
-- the organization-facing product to a singleton.

-- Older APIs allowed zero or multiple playbooks. Do not choose/delete customer
-- configuration implicitly during deploy: fail before changing the schema and
-- require the operator to establish the approved one-per-organization state.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM organizations o
        LEFT JOIN playbooks p ON p.org_id = o.id
        GROUP BY o.id
        HAVING count(p.id) <> 1
    ) THEN
        RAISE EXCEPTION 'migration 0014 requires exactly one playbook per organization; reconcile legacy playbooks before retrying';
    END IF;
END $$;

ALTER TABLE playbooks
    ADD COLUMN pack_id TEXT NOT NULL DEFAULT 'field-service';

-- Organization configuration owns voice only. Pack, policy, tools, and models
-- are code/deployment concerns and must not be restored from this JSON.
UPDATE playbooks
SET config = jsonb_build_object(
        'schema', 2,
        'voice', jsonb_build_object(
            'agent_name', COALESCE(NULLIF(btrim(config #>> '{voice,agent_name}'), ''), 'Dispatch'),
            'tone', COALESCE(NULLIF(btrim(config #>> '{voice,tone}'), ''), 'clear and helpful'),
            'custom_instructions', COALESCE(config #>> '{voice,custom_instructions}', '')
        )
    ),
    version = version + 1;

INSERT INTO config_revisions (
    id, org_id, entity_kind, entity_id, version, config, command_id, actor
)
SELECT 'rev_0014_behavior_' || id, org_id, 'playbook', id, version, config,
       'migration:0014:agent-behavior:' || id, 'migration:0014'
FROM playbooks;

ALTER TABLE playbooks
    ADD CONSTRAINT playbooks_one_per_org UNIQUE (org_id),
    ADD CONSTRAINT playbooks_org_id_id_key UNIQUE (org_id, id);

-- Durable reservation/result record for organization setting commands. The
-- request hash distinguishes a retry from accidental idempotency-key reuse.
CREATE TABLE settings_commands (
    org_id       TEXT NOT NULL REFERENCES organizations (id),
    command_id   TEXT NOT NULL,
    entity_kind  TEXT NOT NULL,
    entity_id    TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    actor         TEXT NOT NULL,
    state         TEXT NOT NULL CHECK (state IN ('pending', 'completed')),
    outcome       TEXT,
    result        JSONB,
    http_status   INT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at  TIMESTAMPTZ,
    PRIMARY KEY (org_id, command_id),
    CHECK (
        (state = 'pending' AND outcome IS NULL AND result IS NULL AND http_status IS NULL AND completed_at IS NULL)
        OR
        (state = 'completed' AND outcome IS NOT NULL AND result IS NOT NULL AND http_status IS NOT NULL AND completed_at IS NOT NULL)
    )
);

-- Backfill the organization singleton before making routing bindings required.
UPDATE channel_connections cc
SET default_playbook_id = pb.id
FROM playbooks pb
WHERE pb.org_id = cc.org_id
  AND cc.default_playbook_id IS NULL;

UPDATE run_bindings rb
SET playbook_id = pb.id
FROM playbooks pb
WHERE pb.org_id = rb.org_id
  AND rb.playbook_id IS NULL;

ALTER TABLE channel_connections ALTER COLUMN default_playbook_id SET NOT NULL;
ALTER TABLE run_bindings ALTER COLUMN playbook_id SET NOT NULL;

-- Composite keys make same-organization relationships database invariants.
ALTER TABLE runs ADD CONSTRAINT runs_org_id_id_key UNIQUE (org_id, id);
ALTER TABLE customers ADD CONSTRAINT customers_org_id_id_key UNIQUE (org_id, id);
ALTER TABLE contact_identities ADD CONSTRAINT contact_identities_org_id_id_key UNIQUE (org_id, id);
ALTER TABLE channel_connections ADD CONSTRAINT channel_connections_org_id_id_key UNIQUE (org_id, id);
ALTER TABLE conversations ADD CONSTRAINT conversations_org_id_id_key UNIQUE (org_id, id);
ALTER TABLE cases ADD CONSTRAINT cases_org_id_id_key UNIQUE (org_id, id);
ALTER TABLE actions ADD CONSTRAINT actions_org_id_id_key UNIQUE (org_id, id);
ALTER TABLE messages ADD CONSTRAINT messages_org_id_id_key UNIQUE (org_id, id);
ALTER TABLE context_snapshots ADD CONSTRAINT context_snapshots_org_id_id_key UNIQUE (org_id, id);

-- Early tables predated the organizations table. Anchor their tenant key now
-- that request identity is organization-scoped.
ALTER TABLE runs ADD CONSTRAINT runs_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE actions ADD CONSTRAINT actions_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE events ADD CONSTRAINT events_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE customers ADD CONSTRAINT customers_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE conversations ADD CONSTRAINT conversations_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE messages ADD CONSTRAINT messages_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE cases ADD CONSTRAINT cases_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE run_bindings ADD CONSTRAINT run_bindings_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE run_messages ADD CONSTRAINT run_messages_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE conversation_events ADD CONSTRAINT conversation_events_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE outbox ADD CONSTRAINT outbox_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE action_commands ADD CONSTRAINT action_commands_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE context_snapshots ADD CONSTRAINT context_snapshots_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
ALTER TABLE model_turns ADD CONSTRAINT model_turns_org_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);

ALTER TABLE channel_connections
    ADD CONSTRAINT channel_connections_org_playbook_fkey
    FOREIGN KEY (org_id, default_playbook_id) REFERENCES playbooks (org_id, id);

ALTER TABLE contact_identities
    ADD CONSTRAINT contact_identities_org_customer_fkey
    FOREIGN KEY (org_id, customer_id) REFERENCES customers (org_id, id);

ALTER TABLE conversations
    ADD CONSTRAINT conversations_org_customer_fkey
    FOREIGN KEY (org_id, customer_id) REFERENCES customers (org_id, id),
    ADD CONSTRAINT conversations_org_channel_fkey
    FOREIGN KEY (org_id, channel_id) REFERENCES channel_connections (org_id, id),
    ADD CONSTRAINT conversations_org_identity_fkey
    FOREIGN KEY (org_id, contact_identity_id) REFERENCES contact_identities (org_id, id);

ALTER TABLE cases
    ADD CONSTRAINT cases_org_customer_fkey
    FOREIGN KEY (org_id, customer_id) REFERENCES customers (org_id, id),
    ADD CONSTRAINT cases_org_conversation_fkey
    FOREIGN KEY (org_id, conversation_id) REFERENCES conversations (org_id, id);

ALTER TABLE run_bindings
    ADD CONSTRAINT run_bindings_org_run_fkey
    FOREIGN KEY (org_id, run_id) REFERENCES runs (org_id, id),
    ADD CONSTRAINT run_bindings_org_conversation_fkey
    FOREIGN KEY (org_id, conversation_id) REFERENCES conversations (org_id, id),
    ADD CONSTRAINT run_bindings_org_case_fkey
    FOREIGN KEY (org_id, case_id) REFERENCES cases (org_id, id),
    ADD CONSTRAINT run_bindings_org_playbook_fkey
    FOREIGN KEY (org_id, playbook_id) REFERENCES playbooks (org_id, id);

ALTER TABLE actions
    ADD CONSTRAINT actions_org_run_fkey
    FOREIGN KEY (org_id, run_id) REFERENCES runs (org_id, id);

ALTER TABLE events
    ADD CONSTRAINT events_org_run_fkey
    FOREIGN KEY (org_id, run_id) REFERENCES runs (org_id, id);

ALTER TABLE messages
    ADD CONSTRAINT messages_org_conversation_fkey
    FOREIGN KEY (org_id, conversation_id) REFERENCES conversations (org_id, id);

ALTER TABLE conversation_events
    ADD CONSTRAINT conversation_events_org_conversation_fkey
    FOREIGN KEY (org_id, conversation_id) REFERENCES conversations (org_id, id),
    ADD CONSTRAINT conversation_events_org_message_fkey
    FOREIGN KEY (org_id, message_id) REFERENCES messages (org_id, id),
    ADD CONSTRAINT conversation_events_org_run_fkey
    FOREIGN KEY (org_id, run_id) REFERENCES runs (org_id, id),
    ADD CONSTRAINT conversation_events_org_case_fkey
    FOREIGN KEY (org_id, case_id) REFERENCES cases (org_id, id);

ALTER TABLE outbox
    ADD CONSTRAINT outbox_org_conversation_fkey
    FOREIGN KEY (org_id, conversation_id) REFERENCES conversations (org_id, id);

ALTER TABLE action_commands
    ADD CONSTRAINT action_commands_org_action_fkey
    FOREIGN KEY (org_id, action_id) REFERENCES actions (org_id, id);

ALTER TABLE context_snapshots
    ADD CONSTRAINT context_snapshots_org_conversation_fkey
    FOREIGN KEY (org_id, conversation_id) REFERENCES conversations (org_id, id),
    ADD CONSTRAINT context_snapshots_org_run_fkey
    FOREIGN KEY (org_id, run_id) REFERENCES runs (org_id, id);

ALTER TABLE model_turns
    ADD CONSTRAINT model_turns_org_conversation_fkey
    FOREIGN KEY (org_id, conversation_id) REFERENCES conversations (org_id, id),
    ADD CONSTRAINT model_turns_org_run_fkey
    FOREIGN KEY (org_id, run_id) REFERENCES runs (org_id, id),
    ADD CONSTRAINT model_turns_org_context_snapshot_fkey
    FOREIGN KEY (org_id, context_snapshot_id) REFERENCES context_snapshots (org_id, id);

ALTER TABLE run_messages
    ADD CONSTRAINT run_messages_org_run_fkey
    FOREIGN KEY (org_id, run_id) REFERENCES runs (org_id, id);
