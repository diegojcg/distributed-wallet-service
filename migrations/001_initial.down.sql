DROP TABLE outbox,inbox,wallet_ledger,wager_transactions,wallets CASCADE;
DROP FUNCTION forbid_mutation(),guard_transaction(),guard_outbox(),check_wallet_integrity(),check_transaction_integrity();
