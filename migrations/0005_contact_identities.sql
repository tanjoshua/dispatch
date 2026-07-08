-- Contact identities (design/004-domain-remodel.md §3): split a customer's
-- channel endpoints out of the customer row. A Customer is the CRM aggregate; a
-- ContactIdentity is one (channel_kind, address) the customer is reachable at.
-- Inbound now resolves (kind, address) -> identity -> customer, so the same
-- person on WhatsApp and SMS is one customer. Uniqueness moves off the
-- customer's phone and onto the identity.

CREATE TABLE contact_identities (
    id           TEXT PRIMARY KEY,
    org_id       TEXT NOT NULL REFERENCES organizations (id),
    customer_id  TEXT NOT NULL REFERENCES customers (id),
    channel_kind TEXT NOT NULL,  -- dev | whatsapp | sms | email — matches a channel connection's kind
    address      TEXT NOT NULL,  -- the customer-side address on that kind (phone, email, dev token)
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, channel_kind, address)
);

CREATE INDEX contact_identities_customer_idx ON contact_identities (customer_id);

-- Backfill one identity per existing customer from their phone, using the kind
-- of the connection their conversations arrived on (all 'dev' in v1; resolved
-- generically here so this stays correct if other kinds already exist). Readable
-- backfill id, matching the seeded-row convention (org_dev / chan_dev).
INSERT INTO contact_identities (id, org_id, customer_id, channel_kind, address)
SELECT 'ci_' || c.id, c.org_id, c.id,
       COALESCE(
         (SELECT cc.kind
            FROM conversations conv
            JOIN channel_connections cc ON cc.id = conv.channel_id
           WHERE conv.customer_id = c.id
           ORDER BY conv.created_at
           LIMIT 1),
         'dev'),
       c.phone
FROM customers c
WHERE c.phone <> '';

-- Phone is no longer a property of the customer; it lives on the identity. The
-- UNIQUE (org_id, phone) constraint drops with the column.
ALTER TABLE customers DROP COLUMN phone;
