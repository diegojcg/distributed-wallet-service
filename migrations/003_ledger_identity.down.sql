DROP TRIGGER ledger_context ON wallet_ledger;
DROP FUNCTION guard_ledger_context();
DROP TRIGGER wallet_identity ON wallets;
DROP FUNCTION guard_wallet_identity();
ALTER TABLE wallet_ledger DROP CONSTRAINT one_ledger_per_transaction;
