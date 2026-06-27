CREATE TABLE identities (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id         UUID NOT NULL UNIQUE REFERENCES customers(id) ON DELETE CASCADE,
    bvn_masked          TEXT,                        -- e.g. "***456"
    nin_masked          TEXT,                        -- e.g. "***789"
    kyc_tier            SMALLINT NOT NULL DEFAULT 1
                            CHECK (kyc_tier BETWEEN 1 AND 3),
    verification_status TEXT NOT NULL DEFAULT 'pending'
                            CHECK (verification_status IN ('pending', 'verified', 'failed')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- FR-2.3: at least one of BVN or NIN must be present
    CONSTRAINT identity_requires_bvn_or_nin
        CHECK (bvn_masked IS NOT NULL OR nin_masked IS NOT NULL)
);
