-- Organization & channel connections (design/002-organization-and-channels.md):
-- promote org from a server-global constant to a real row, and split the bare
-- "channel" string into a channel *connection* an org owns. Org identity now
-- rides on the connection an inbound message arrives on, not on the process.
--
-- Seeds one org (org_dev) and one dev channel connection (chan_dev) so existing
-- data and every `go run` keep working with no manual steps.

CREATE TABLE organizations (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    settings   JSONB NOT NULL DEFAULT '{}', -- open bag for small org-level settings; typed fields graduate out as they earn it
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE channel_connections (
    id         TEXT PRIMARY KEY,
    org_id     TEXT NOT NULL REFERENCES organizations (id),
    kind       TEXT NOT NULL,               -- dev | whatsapp | sms | email — selects the adapter
    address    TEXT NOT NULL,               -- business-side identity inbound is addressed to; inbound lookup key
    config     JSONB NOT NULL DEFAULT '{}', -- per-kind config/credentials (empty for dev)
    status     TEXT NOT NULL DEFAULT 'active', -- active | disabled
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (kind, address)
);

CREATE INDEX channel_connections_org_idx ON channel_connections (org_id);

-- Seed the dev org and its dev channel connection (white-glove onboarding for
-- now — seeded records, not a signup flow).
INSERT INTO organizations (id, name) VALUES ('org_dev', 'Dev Org');
INSERT INTO channel_connections (id, org_id, kind, address)
    VALUES ('chan_dev', 'org_dev', 'dev', 'dev');

-- A conversation now references the connection it belongs to, replacing the
-- bare channel string name. Add nullable, backfill every existing conversation
-- to the seeded dev connection, then enforce and drop the old column.
ALTER TABLE conversations ADD COLUMN channel_id TEXT REFERENCES channel_connections (id);
UPDATE conversations SET channel_id = 'chan_dev' WHERE channel_id IS NULL;
ALTER TABLE conversations ALTER COLUMN channel_id SET NOT NULL;
ALTER TABLE conversations DROP COLUMN channel;
