-- Escalation (design/001-escalation.md): a conversation-level attention state,
-- projected from the agent's escalate action and the dispatcher's acknowledge.
-- The append-only event log stays the source of truth; these columns are the
-- current-view projection the dispatcher UI reads.
--
-- Nullable/defaulted so every existing conversation is 'none' (never escalated).

ALTER TABLE conversations
    ADD COLUMN attention_state  TEXT NOT NULL DEFAULT 'none', -- none | flagged | acknowledged
    ADD COLUMN attention_reason TEXT NOT NULL DEFAULT '',     -- the agent's one-line reason, shown to the dispatcher
    ADD COLUMN escalated_at     TIMESTAMPTZ;                  -- when the current attention episode was raised

-- Flagged conversations are the ones a dispatcher must look at first.
CREATE INDEX conversations_attention_idx ON conversations (org_id, attention_state);
