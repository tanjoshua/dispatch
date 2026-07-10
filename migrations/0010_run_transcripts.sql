-- Run transcripts in Postgres + rolling thread summary (OVERVIEW §6.1 #3, §6.4).
--
-- The agent loop's conversation context used to ride inside Temporal history:
-- every Complete activity input embedded the whole transcript, growing history
-- O(n²) toward the payload limit. The transcript now lives here, one row per
-- message, with workflow-assigned sequence numbers so appends are idempotent
-- under activity retries and deterministic under replay.

CREATE TABLE run_messages (
    run_id     TEXT NOT NULL REFERENCES runs (id),
    seq        INT  NOT NULL, -- transcript position, assigned by the workflow
    org_id     TEXT NOT NULL,
    message    JSONB NOT NULL, -- one llm.Message
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, seq)
);

-- The thread's rolling summary: one dated line per completed task, taken from
-- the dispatcher-approved close_case summary. Briefings feed it to fresh runs
-- so a returning customer isn't met cold.
ALTER TABLE conversations ADD COLUMN thread_summary TEXT NOT NULL DEFAULT '';
