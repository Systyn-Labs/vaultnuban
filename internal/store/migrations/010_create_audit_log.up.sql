-- Immutable audit trail for every state-changing action (NFR-5).
-- Rows are never updated or deleted.
CREATE TABLE audit_log (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID REFERENCES tenants(id),
    actor       TEXT NOT NULL,               -- API key prefix or "system"
    action      TEXT NOT NULL,               -- e.g. "provision_va", "rename_va", "close_va", "kyc_tier_change", "suspense_resolve"
    entity_type TEXT NOT NULL,               -- e.g. "virtual_account", "customer", "suspense_item"
    entity_id   TEXT NOT NULL,               -- UUID or string ID of affected entity
    before_after JSONB,                      -- {before: {...}, after: {...}}
    at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_entity  ON audit_log(entity_type, entity_id);
CREATE INDEX idx_audit_tenant  ON audit_log(tenant_id);
CREATE INDEX idx_audit_at      ON audit_log(at DESC);
