-- Extend transactions.source to include 'internal' for compensating entries
-- posted during suspense resolution (not sourced from Nomba).
ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_source_check,
    ADD CONSTRAINT transactions_source_check
        CHECK (source IN ('webhook', 'sweep', 'internal'));
