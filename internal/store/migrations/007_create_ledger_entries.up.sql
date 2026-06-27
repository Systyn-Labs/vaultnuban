-- Ledger account names:
--   customer_wallet:{uuid}   — customer's virtual balance
--   nomba_settlement         — control account representing funds at Nomba
--   suspense                 — unmatched / held funds
--   fee_income               — fee revenue

CREATE TABLE ledger_entries (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id TEXT NOT NULL REFERENCES transactions(id),
    account        TEXT NOT NULL,                   -- see account name conventions above
    direction      TEXT NOT NULL CHECK (direction IN ('debit', 'credit')),
    amount_kobo    BIGINT NOT NULL CHECK (amount_kobo > 0),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- No UPDATE or DELETE path exists in code; the table is append-only.
-- Per-transaction balance check (Σdebits = Σcredits) is enforced in the
-- ledger engine and verified by the reconciliation harness (NFR-1).

CREATE INDEX idx_ledger_transaction ON ledger_entries(transaction_id);
CREATE INDEX idx_ledger_account     ON ledger_entries(account);
CREATE INDEX idx_ledger_created_at  ON ledger_entries(created_at DESC);
