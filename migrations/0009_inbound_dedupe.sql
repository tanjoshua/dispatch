-- Inbound dedupe + thread/run uniqueness (OVERVIEW §6.1 #2).
--
-- WhatsApp-style providers retry webhooks and can deliver duplicates. Inbound
-- messages now carry the provider's message ID and dedupe on it per
-- conversation; outbound rows leave it NULL. The thread invariant — one
-- conversation per (customer, channel connection) — becomes a real constraint
-- so two concurrent first messages can't fork the thread.

ALTER TABLE messages ADD COLUMN provider_message_id TEXT;

CREATE UNIQUE INDEX messages_provider_message_id_key
    ON messages (conversation_id, provider_message_id)
    WHERE provider_message_id IS NOT NULL;

-- Fold any duplicate threads (artifacts of the very race this index closes)
-- into the earliest one — that is the customer's true persistent thread —
-- before constraining. Children (messages, run bindings, cases) move with it.
CREATE TEMP TABLE duplicate_conversations ON COMMIT DROP AS
SELECT id, keep_id FROM (
    SELECT id, first_value(id) OVER (
               PARTITION BY org_id, customer_id, channel_id
               ORDER BY created_at, id) AS keep_id
    FROM conversations
) ranked
WHERE id <> keep_id;

UPDATE messages m SET conversation_id = d.keep_id
FROM duplicate_conversations d WHERE m.conversation_id = d.id;

UPDATE run_bindings rb SET conversation_id = d.keep_id
FROM duplicate_conversations d WHERE rb.conversation_id = d.id;

UPDATE cases c SET conversation_id = d.keep_id
FROM duplicate_conversations d WHERE c.conversation_id = d.id;

DELETE FROM conversations WHERE id IN (SELECT id FROM duplicate_conversations);

CREATE UNIQUE INDEX conversations_customer_channel_key
    ON conversations (org_id, customer_id, channel_id);
