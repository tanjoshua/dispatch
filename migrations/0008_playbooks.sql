-- Playbooks (design/004-domain-remodel.md §8): a playbook is the org-tailored
-- config a channel connection routes inbound to. It selects the code agent
-- (pack) that runs and names the case type that pack produces. With one pack
-- (field service) this is a real *selection* seam — the binding point the whole
-- horizontal story hangs on — not yet the full pack SDK (config-parameterized
-- prompts/schemas), which is its own doc (005).

CREATE TABLE playbooks (
    id         TEXT PRIMARY KEY,
    org_id     TEXT NOT NULL REFERENCES organizations (id),
    name       TEXT NOT NULL,
    agent      TEXT NOT NULL,               -- names a code-registered agent definition (the pack)
    case_type  TEXT NOT NULL,               -- the case type this playbook produces
    config     JSONB NOT NULL DEFAULT '{}', -- per-playbook parameters (prompt/schema tuning) — grows in 005
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX playbooks_org_idx ON playbooks (org_id);

-- Seed the field-service playbook (selects the code "intake" agent).
INSERT INTO playbooks (id, org_id, name, agent, case_type)
    VALUES ('pb_field_service', 'org_dev', 'Field Service Intake', 'intake', 'field_service_job');

-- A channel connection routes inbound to a playbook.
ALTER TABLE channel_connections ADD COLUMN default_playbook_id TEXT REFERENCES playbooks (id);
UPDATE channel_connections SET default_playbook_id = 'pb_field_service' WHERE kind = 'dev';

-- A run records the playbook it runs under (audit; and the case type it produces
-- is derived from it).
ALTER TABLE run_bindings ADD COLUMN playbook_id TEXT REFERENCES playbooks (id);
UPDATE run_bindings SET playbook_id = 'pb_field_service' WHERE playbook_id IS NULL;
