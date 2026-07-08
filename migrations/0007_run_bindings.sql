-- Run per task + persistent threads + many cases per thread
-- (design/004-domain-remodel.md §4, §6). A run is one agent *task* (an intake),
-- bound to the conversation it serves and the case it produces. A thread now
-- persists across many runs and can carry many cases. The app owns the
-- run<->(conversation, case) binding in its own table; agentkit's `runs` table
-- stays business-agnostic.

-- Many cases per thread: drop the transitional 1:1 constraint. (The constraint
-- kept its original name through the jobs->cases rename.)
ALTER TABLE cases DROP CONSTRAINT jobs_conversation_id_key;

-- run -> (conversation, case) binding. case_id is null until the run's first
-- update_case creates the case it is working; task_kind is 'intake' for now
-- (scheduling / follow-up tasks land later).
CREATE TABLE run_bindings (
    run_id          TEXT PRIMARY KEY REFERENCES runs (id),
    org_id          TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations (id),
    case_id         TEXT REFERENCES cases (id),
    task_kind       TEXT NOT NULL DEFAULT 'intake',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX run_bindings_conversation_idx ON run_bindings (conversation_id, created_at DESC);

-- Backfill bindings from the existing single-run pointer and its (1:1) case,
-- before dropping the pointer.
INSERT INTO run_bindings (run_id, org_id, conversation_id, case_id)
SELECT conv.run_id, conv.org_id, conv.id, ca.id
FROM conversations conv
LEFT JOIN cases ca ON ca.conversation_id = conv.id
WHERE conv.run_id IS NOT NULL;

-- The single-run pointer is replaced by run_bindings.
ALTER TABLE conversations DROP COLUMN run_id;

-- Threads are persistent now; nothing closes them on case completion. Reset the
-- projection so no legacy thread shows as closed (the status column stays for a
-- future archive state).
UPDATE conversations SET status = 'open' WHERE status <> 'open';
