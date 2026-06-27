CREATE TABLE virtual_accounts (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id      UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    nomba_account_ref TEXT NOT NULL UNIQUE,    -- deterministic: t{tenant}c{ulid}
    nuban            TEXT NOT NULL,            -- 10-digit NUBAN; never recycled
    bank_name        TEXT NOT NULL,
    account_name     TEXT NOT NULL,
    nomba_holder_id  TEXT,                     -- accountHolderId returned by Nomba
    status           TEXT NOT NULL DEFAULT 'PENDING'
                         CHECK (status IN ('PENDING', 'ACTIVE', 'SUSPENDED', 'CLOSED')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- FR-3.4: exactly one ACTIVE virtual account per customer
CREATE UNIQUE INDEX idx_va_one_active_per_customer
    ON virtual_accounts(customer_id)
    WHERE status = 'ACTIVE';

-- NUBAN must never be reassigned — enforced by this unique index across all rows
CREATE UNIQUE INDEX idx_va_nuban_unique ON virtual_accounts(nuban);

CREATE INDEX idx_va_customer ON virtual_accounts(customer_id);
CREATE INDEX idx_va_status   ON virtual_accounts(status);
