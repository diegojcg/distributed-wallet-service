-- A transaction belongs to exactly one wallet and can have only one financial entry.
ALTER TABLE wallet_ledger ADD CONSTRAINT one_ledger_per_transaction UNIQUE(transaction_id);
CREATE FUNCTION guard_wallet_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.version<>1 THEN RAISE EXCEPTION 'initial wallet version must be one' USING ERRCODE='23514'; END IF;
 ELSE
  IF (NEW.id,NEW.player_id,NEW.currency,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.player_id,OLD.currency,OLD.created_at) THEN
   RAISE EXCEPTION 'wallet identity is immutable' USING ERRCODE='23514';
  END IF;
  IF (NEW.balance=OLD.balance AND NEW.version<>OLD.version) OR
     (NEW.balance<>OLD.balance AND NEW.version::numeric<>OLD.version::numeric+1) THEN
   RAISE EXCEPTION 'wallet version must track balance changes' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER wallet_identity BEFORE INSERT OR UPDATE ON wallets FOR EACH ROW EXECUTE FUNCTION guard_wallet_identity();
CREATE FUNCTION guard_ledger_context() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE t wager_transactions%ROWTYPE; expected_first bigint;
BEGIN
 -- Serialize direct SQL writers too; financial consistency remains checked at commit.
 PERFORM id FROM wallets WHERE id=NEW.wallet_id FOR UPDATE;
 SELECT * INTO t FROM wager_transactions WHERE id=NEW.transaction_id;
 IF t.id IS NULL OR t.wallet_id<>NEW.wallet_id OR t.currency<>NEW.currency THEN
  RAISE EXCEPTION 'ledger transaction context mismatch' USING ERRCODE='23514';
 END IF;
 IF t.kind='OPENING' AND (NEW.wallet_version<>1 OR NEW.balance_before<>0) THEN
  RAISE EXCEPTION 'opening must be first entry at version one' USING ERRCODE='23514';
 END IF;
 expected_first:=CASE WHEN t.kind='OPENING' THEN 1 ELSE 2 END;
 IF NOT EXISTS(SELECT 1 FROM wallet_ledger WHERE wallet_id=NEW.wallet_id) AND NEW.wallet_version<>expected_first THEN
  RAISE EXCEPTION 'invalid first ledger version' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ledger_context BEFORE INSERT ON wallet_ledger FOR EACH ROW EXECUTE FUNCTION guard_ledger_context();
