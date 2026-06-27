CREATE TABLE transactions (
    id                  TEXT PRIMARY KEY,           -- Nomba transactionId (FR-5.3)
    virtual_account_id  UUID REFERENCES virtual_accounts(id),  -- NULL if unmatched → suspense
    session_id          TEXT,                        -- NIP session ID for requery
    amount_kobo         BIGINT NOT NULL CHECK (amount_kobo > 0),
    direction           TEXT NOT NULL DEFAULT 'credit'
                            CHECK (direction IN ('credit', 'debit')),
    source              TEXT NOT NULL
                            CHECK (source IN ('webhook', 'sweep')),
    status              TEXT NOT NULL DEFAULT 'posted'
                            CHECK (status IN ('posted', 'reversed', 'pending')),
    sender_name         TEXT,
    sender_bank         TEXT,
    narration           TEXT,
    raw                 JSONB NOT NULL,              -- full Nomba payload
    occurred_at         TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_txn_virtual_account ON transactions(virtual_account_id);
CREATE INDEX idx_txn_occurred_at     ON transactions(occurred_at DESC);
CREATE INDEX idx_txn_session_id      ON transactions(session_id) WHERE session_id IS NOT NULL;
