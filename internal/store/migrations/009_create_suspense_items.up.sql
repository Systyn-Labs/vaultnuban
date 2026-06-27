CREATE TABLE suspense_items (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id TEXT NOT NULL REFERENCES transactions(id),
    reason         TEXT NOT NULL
                       CHECK (reason IN ('unmatched', 'closed_account', 'amount_mismatch', 'tier_limit', 'suspended_account')),
    status         TEXT NOT NULL DEFAULT 'open'
                       CHECK (status IN ('open', 'reassigned', 'refund_flagged')),
    resolved_by    TEXT,                        -- actor (tenant API key prefix or "ops")
    resolved_at    TIMESTAMPTZ,
    notes          TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_suspense_status         ON suspense_items(status);
CREATE INDEX idx_suspense_transaction_id ON suspense_items(transaction_id);
