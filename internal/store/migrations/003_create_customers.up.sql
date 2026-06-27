CREATE TABLE customers (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    external_ref TEXT NOT NULL,
    display_name TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'active'
                     CHECK (status IN ('active', 'inactive')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (tenant_id, external_ref)
);

CREATE INDEX idx_customers_tenant ON customers(tenant_id);
