-- P1: delivery attempt log per relay endpoint (FR-11.2, FR-11.3)
CREATE TABLE relay_deliveries (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint_id  UUID NOT NULL REFERENCES relay_endpoints(id) ON DELETE CASCADE,
    event_type   TEXT NOT NULL,
    payload      JSONB NOT NULL,
    attempt      SMALLINT NOT NULL DEFAULT 1,
    status       TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'delivered', 'failed', 'dead_letter')),
    status_code  SMALLINT,
    error        TEXT,
    next_retry_at TIMESTAMPTZ,
    delivered_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_relay_del_endpoint ON relay_deliveries(endpoint_id);
CREATE INDEX idx_relay_del_status   ON relay_deliveries(status);
CREATE INDEX idx_relay_del_retry    ON relay_deliveries(next_retry_at)
    WHERE status = 'failed';
