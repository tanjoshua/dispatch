-- Job -> Case (design/004-domain-remodel.md §5): generalize the field-service
-- "job" record into a neutral Case with a typed core plus a per-vertical `data`
-- bag. The field-service fields (address, issue, urgency) move into `data`; the
-- customer's name and phone are no longer copied here (name lives on the
-- customer, contact on the identity — Phase 1a). A `type` discriminates the
-- vertical; `customer_id` makes the case reference its customer directly.
--
-- Transitional: the case stays one-per-conversation (UNIQUE(conversation_id)
-- carries over from the rename). Many-cases-per-thread arrives in Phase 3, where
-- run-binding defines which case a run is working.

ALTER TABLE jobs RENAME TO cases;

ALTER TABLE cases ADD COLUMN customer_id TEXT REFERENCES customers (id);
ALTER TABLE cases ADD COLUMN type        TEXT NOT NULL DEFAULT 'field_service_job';
ALTER TABLE cases ADD COLUMN data        JSONB NOT NULL DEFAULT '{}';

-- Reference the customer directly (backfill from the conversation).
UPDATE cases c SET customer_id = conv.customer_id
FROM conversations conv WHERE conv.id = c.conversation_id;
ALTER TABLE cases ALTER COLUMN customer_id SET NOT NULL;

-- Move the field-service fields into the data bag, keeping only non-empty keys
-- (an empty field was "not collected", which is absence, not an empty string).
UPDATE cases SET data =
      CASE WHEN address <> '' THEN jsonb_build_object('address', address) ELSE '{}'::jsonb END
   || CASE WHEN issue   <> '' THEN jsonb_build_object('issue',   issue)   ELSE '{}'::jsonb END
   || CASE WHEN urgency <> '' THEN jsonb_build_object('urgency', urgency) ELSE '{}'::jsonb END;

-- Drop the columns now living in `data` or on the customer/identity.
ALTER TABLE cases DROP COLUMN customer_name;
ALTER TABLE cases DROP COLUMN phone;
ALTER TABLE cases DROP COLUMN address;
ALTER TABLE cases DROP COLUMN issue;
ALTER TABLE cases DROP COLUMN urgency;
