ALTER TABLE wager_transactions ADD COLUMN correlation_id text NOT NULL DEFAULT 'legacy';
ALTER TABLE wager_transactions ADD COLUMN causation_id text;
ALTER TABLE wager_transactions DISABLE TRIGGER transaction_transition;
UPDATE wager_transactions SET correlation_id=id::text;
SET CONSTRAINTS ALL IMMEDIATE;
ALTER TABLE wager_transactions ENABLE TRIGGER transaction_transition;
ALTER TABLE wager_transactions ALTER COLUMN correlation_id DROP DEFAULT;
ALTER TABLE wager_transactions ADD CONSTRAINT correlation_id_valid CHECK(length(correlation_id) BETWEEN 1 AND 128);
ALTER TABLE wager_transactions ADD CONSTRAINT causation_id_valid CHECK(causation_id IS NULL OR length(causation_id) BETWEEN 1 AND 200);
