-- FR-6.5: sweep run log for demo/audit
CREATE TABLE sweep_runs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    window_from  TIMESTAMPTZ NOT NULL,
    window_to    TIMESTAMPTZ NOT NULL,
    pages_fetched INT NOT NULL DEFAULT 0,
    found        INT NOT NULL DEFAULT 0,
    posted       INT NOT NULL DEFAULT 0,
    suspensed    INT NOT NULL DEFAULT 0,
    duration_ms  INT,
    error        TEXT,
    ran_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sweep_ran_at ON sweep_runs(ran_at DESC);
