CREATE TABLE collections (
    id                   TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    customer_id          UUID NOT NULL REFERENCES customers(id),
    virtual_account_id   UUID REFERENCES virtual_accounts(id),
    expected_amount_kobo BIGINT,
    reference            TEXT NOT NULL,
    description          TEXT,
    status               TEXT NOT NULL DEFAULT 'open',
    expires_at           TIMESTAMPTZ,
    fulfilled_by_txn_id  TEXT REFERENCES transactions(id),
    fulfilled_at         TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX collections_reference_customer_idx ON collections(customer_id, reference);
CREATE INDEX collections_customer_id_status_idx ON collections(customer_id, status);
CREATE INDEX collections_va_open_idx ON collections(virtual_account_id, status) WHERE status = 'open';
