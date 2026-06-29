CREATE TABLE withdrawals (
    id                         TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    customer_id                UUID NOT NULL REFERENCES customers(id),
    virtual_account_id         UUID REFERENCES virtual_accounts(id),
    amount_kobo                BIGINT NOT NULL CHECK (amount_kobo > 0),
    destination_bank_code      TEXT NOT NULL,
    destination_account_number TEXT NOT NULL,
    destination_account_name   TEXT NOT NULL,
    narration                  TEXT,
    status                     TEXT NOT NULL DEFAULT 'pending',
    provider_transaction_id    TEXT,
    provider_session_id        TEXT,
    failure_reason             TEXT,
    raw                        JSONB,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX withdrawals_customer_id_idx ON withdrawals(customer_id);
CREATE INDEX withdrawals_status_idx ON withdrawals(status);
