-- Message authorship (design/003-dispatcher-as-participant.md): the dispatcher
-- is a first-class participant who can message the customer directly, so an
-- outbound message is no longer necessarily the agent's. Record who authored
-- each message. `direction` stays (inbound = customer; outbound = agent or
-- dispatcher); `author` is the richer field the UI and the agent context key on.
--
-- Backfill: every existing inbound is the customer; every existing outbound was
-- an agent send (the only outbound path that existed before this doc).

ALTER TABLE messages ADD COLUMN author TEXT NOT NULL DEFAULT 'agent'; -- customer | agent | dispatcher
UPDATE messages SET author = 'customer' WHERE direction = 'inbound';
