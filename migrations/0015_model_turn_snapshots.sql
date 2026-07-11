-- Persist the exact request/response pair for each completion and link every
-- resulting Action back to the immutable model-turn snapshot that produced it.

ALTER TABLE model_turns ADD COLUMN seq INT;

WITH ranked AS (
    SELECT id, row_number() OVER (PARTITION BY run_id ORDER BY created_at,id)::INT AS seq
    FROM model_turns
)
UPDATE model_turns mt SET seq=ranked.seq FROM ranked WHERE ranked.id=mt.id;

ALTER TABLE model_turns ALTER COLUMN seq SET NOT NULL;
ALTER TABLE model_turns ADD CONSTRAINT model_turns_run_seq_key UNIQUE (run_id,seq);
ALTER TABLE model_turns ADD CONSTRAINT model_turns_org_id_id_key UNIQUE (org_id,id);

ALTER TABLE actions ADD COLUMN model_turn_id TEXT;
ALTER TABLE actions ADD CONSTRAINT actions_org_model_turn_fkey
    FOREIGN KEY (org_id,model_turn_id) REFERENCES model_turns(org_id,id);
CREATE INDEX actions_model_turn_idx ON actions(model_turn_id) WHERE model_turn_id IS NOT NULL;

-- App mutations use the Action ID as their retry root. One Action may emit a
-- stage event plus its domain event, but never the same event type twice.
ALTER TABLE conversation_events ADD COLUMN action_id TEXT;
ALTER TABLE conversation_events ADD CONSTRAINT conversation_events_org_action_fkey
    FOREIGN KEY (org_id,action_id) REFERENCES actions(org_id,id);
CREATE UNIQUE INDEX conversation_events_action_type_key
    ON conversation_events(action_id,type) WHERE action_id IS NOT NULL;
