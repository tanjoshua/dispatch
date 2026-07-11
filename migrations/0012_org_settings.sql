-- Organization-configurable playbooks, knowledge, and channel routing.

ALTER TABLE organizations ADD COLUMN version BIGINT NOT NULL DEFAULT 1;
ALTER TABLE channel_connections ADD COLUMN version BIGINT NOT NULL DEFAULT 1;

CREATE TABLE config_revisions (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL REFERENCES organizations (id),
    entity_kind TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    version     BIGINT NOT NULL,
    config      JSONB NOT NULL,
    command_id  TEXT NOT NULL,
    actor       TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, command_id),
    UNIQUE (entity_kind, entity_id, version)
);

UPDATE playbooks
SET config = '{
  "schema": 1,
  "pack": "field-service",
  "model_tier": "best",
  "model_override": "",
  "voice": {"agent_name": "Dispatch", "tone": "clear and helpful", "custom_instructions": ""},
  "policy": {
    "inquiry": {"propose_response": "require_review"},
    "service_job": {
      "propose_response": "require_review",
      "create_case": "auto",
      "select_case": "auto",
      "update_case": "auto"
    }
  }
}'::jsonb
WHERE id = 'pb_field_service';

UPDATE organizations
SET settings = jsonb_set(settings, '{profile}', '{
  "business_name": "Brightside Home Services",
  "hours": "Monday–Friday, 8am–6pm; Saturday, 9am–1pm; closed Sunday",
  "service_area": "Singapore",
  "facts": [
    {"id": "fact_emergency", "label": "Emergencies", "text": "For suspected gas leaks, leave the property and call emergency services before contacting us."},
    {"id": "fact_quotes", "label": "Quotes", "text": "A dispatcher confirms pricing after reviewing the job details."}
  ]
}'::jsonb, true)
WHERE id = 'org_dev';

INSERT INTO config_revisions (id, org_id, entity_kind, entity_id, version, config, command_id, actor)
SELECT 'rev_0012_playbook', org_id, 'playbook', id, version, config,
       'migration:0012:playbook:' || id, 'migration:0012'
FROM playbooks WHERE id = 'pb_field_service';

INSERT INTO config_revisions (id, org_id, entity_kind, entity_id, version, config, command_id, actor)
SELECT 'rev_0012_org', id, 'organization', id, version, settings,
       'migration:0012:organization:' || id, 'migration:0012'
FROM organizations WHERE id = 'org_dev';
