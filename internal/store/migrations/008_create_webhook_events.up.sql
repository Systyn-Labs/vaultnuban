CREATE TABLE webhook_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- FR-4.3: dedupe_key = transactionId + ":" + event_type
    dedupe_key      TEXT NOT NULL UNIQUE,
    event_type      TEXT NOT NULL,
    signature_valid BOOLEAN NOT NULL,
    status          TEXT NOT NULL DEFAULT 'received'
                        CHECK (status IN ('received', 'processed', 'ignored', 'failed')),
    payload         JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at    TIMESTAMPTZ
);

CREATE INDEX idx_webhook_status     ON webhook_events(status);
CREATE INDEX idx_webhook_created_at ON webhook_events(created_at DESC);
