-- P1: tenant-registered webhook relay endpoints (FR-11)
CREATE TABLE relay_endpoints (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    url         TEXT NOT NULL,
    secret_hash TEXT NOT NULL,   -- HMAC signing secret, stored hashed
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_relay_ep_tenant ON relay_endpoints(tenant_id);
