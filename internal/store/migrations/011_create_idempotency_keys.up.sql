-- FR-9.1: Idempotency-Key store backed by Postgres for durability.
-- Redis is the primary fast-path; this table is the durable fallback.
CREATE TABLE idempotency_keys (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    idem_key    TEXT NOT NULL,
    method      TEXT NOT NULL,
    path        TEXT NOT NULL,
    status_code SMALLINT,
    response    JSONB,
    locked      BOOLEAN NOT NULL DEFAULT TRUE,   -- TRUE while request is in-flight
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours',

    UNIQUE (tenant_id, idem_key)
);

CREATE INDEX idx_idem_expires ON idempotency_keys(expires_at);
