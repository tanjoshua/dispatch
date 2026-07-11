ALTER TABLE run_bindings
ADD COLUMN stage TEXT NOT NULL DEFAULT 'triage'
CHECK (stage IN ('triage', 'inquiry', 'service_job', 'quote_request'));

UPDATE run_bindings SET stage='service_job' WHERE case_id IS NOT NULL;

UPDATE playbooks SET config = config - 'model_tier' - 'model_override', version=version+1
WHERE id='pb_field_service';

INSERT INTO config_revisions (id, org_id, entity_kind, entity_id, version, config, command_id, actor)
SELECT 'rev_0013_playbook', org_id, 'playbook', id, version, config,
       'migration:0013:playbook:' || id, 'migration:0013'
FROM playbooks WHERE id='pb_field_service';
