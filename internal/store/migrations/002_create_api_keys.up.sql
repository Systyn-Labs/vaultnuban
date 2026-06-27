CREATE TABLE api_keys (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    key_hash   TEXT NOT NULL UNIQUE,   -- SHA-256 of the full key
    key_prefix TEXT NOT NULL,          -- first 8 chars, displayable
    active     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_api_keys_tenant ON api_keys(tenant_id);
