ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_source_check,
    ADD CONSTRAINT transactions_source_check
        CHECK (source IN ('webhook', 'sweep'));
