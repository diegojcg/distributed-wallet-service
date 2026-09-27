CREATE TABLE wallets (
 id uuid PRIMARY KEY,
 player_id uuid NOT NULL,
 currency text NOT NULL CHECK (currency = 'BRL'),
 balance bigint NOT NULL CHECK (balance >= 0),
 version bigint NOT NULL CHECK (version >= 1),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL CHECK (updated_at >= created_at),
 UNIQUE (player_id, currency),
 UNIQUE (id, player_id, currency)
);
CREATE TABLE wager_transactions (
 id uuid PRIMARY KEY,
 wallet_id uuid NOT NULL,
 player_id uuid NOT NULL,
 currency text NOT NULL,
 kind text NOT NULL CHECK (kind IN ('OPENING','BET','WIN','LOSS','REFUND','ROLLBACK')),
 amount bigint NOT NULL CHECK (amount >= 0),
 provider_id text,
 external_id text,
 idempotency_key text,
 payload_hash text,
 round_id text,
 game_id text,
 reference_external_id text,
 resolved_reference_id uuid REFERENCES wager_transactions(id),
 status text NOT NULL CHECK (status IN ('PENDING','PENDING_REFERENCE','PROCESSED','REJECTED','FAILED')),
 failure_code text,
 result_balance bigint CHECK (result_balance >= 0),
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 next_attempt_at timestamptz,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL CHECK (updated_at >= created_at),
 FOREIGN KEY (wallet_id, player_id, currency) REFERENCES wallets(id, player_id, currency),
 UNIQUE (provider_id,idempotency_key),
 UNIQUE (provider_id,external_id),
 CHECK ((kind='OPENING' AND provider_id IS NULL AND external_id IS NULL AND idempotency_key IS NULL AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL AND reference_external_id IS NULL AND resolved_reference_id IS NULL AND status='PROCESSED') OR
        (kind<>'OPENING' AND length(provider_id)>0 AND provider_id IS NOT NULL AND length(external_id)>0 AND external_id IS NOT NULL AND length(idempotency_key)>0 AND idempotency_key IS NOT NULL AND payload_hash ~ '^[0-9a-f]{64}$' AND payload_hash IS NOT NULL AND length(round_id)>0 AND round_id IS NOT NULL AND length(game_id)>0 AND game_id IS NOT NULL)),
 CHECK ((kind='LOSS' AND amount=0) OR (kind<>'LOSS' AND amount>0)),
 CHECK (kind NOT IN ('REFUND','ROLLBACK') OR (reference_external_id IS NOT NULL AND length(reference_external_id)>0)),
 CHECK (kind NOT IN ('BET','LOSS') OR reference_external_id IS NULL),
 CHECK (reference_external_id IS NULL OR reference_external_id<>external_id),
 CHECK ((status IN ('REJECTED','FAILED') AND failure_code IS NOT NULL) OR (status NOT IN ('REJECTED','FAILED') AND failure_code IS NULL)),
 CHECK (status<>'PROCESSED' OR result_balance IS NOT NULL),
 CHECK (status<>'PENDING_REFERENCE' OR (reference_external_id IS NOT NULL AND next_attempt_at IS NOT NULL)),
 CHECK (kind NOT IN ('REFUND','ROLLBACK') OR status<>'PROCESSED' OR resolved_reference_id IS NOT NULL)
);
CREATE UNIQUE INDEX one_opening_per_wallet ON wager_transactions(wallet_id) WHERE kind='OPENING';
CREATE UNIQUE INDEX one_successful_reversal ON wager_transactions(resolved_reference_id) WHERE status='PROCESSED' AND kind IN ('REFUND','ROLLBACK');
CREATE INDEX pending_references ON wager_transactions(next_attempt_at) WHERE status='PENDING_REFERENCE';
CREATE TABLE wallet_ledger (
 id uuid PRIMARY KEY,
 wallet_id uuid NOT NULL REFERENCES wallets(id),
 transaction_id uuid NOT NULL REFERENCES wager_transactions(id),
 direction text NOT NULL CHECK (direction IN ('DEBIT','CREDIT')),
 amount bigint NOT NULL CHECK (amount>0),
 currency text NOT NULL CHECK (currency='BRL'),
 balance_before bigint NOT NULL CHECK (balance_before>=0),
 balance_after bigint NOT NULL CHECK (balance_after>=0),
 wallet_version bigint NOT NULL CHECK (wallet_version>=1),
 created_at timestamptz NOT NULL,
 UNIQUE (wallet_id,transaction_id),
 UNIQUE (wallet_id,wallet_version),
 CHECK (balance_after::numeric=balance_before::numeric+CASE WHEN direction='CREDIT' THEN amount::numeric ELSE -amount::numeric END)
);
CREATE TABLE inbox (
 consumer_name text NOT NULL,
 message_id text NOT NULL,
 payload_hash text NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
 received_at timestamptz NOT NULL,
 completed_at timestamptz NOT NULL,
 PRIMARY KEY (consumer_name,message_id)
);
CREATE TABLE outbox (
 event_id uuid PRIMARY KEY,
 aggregate_id uuid NOT NULL,
 event_type text NOT NULL CHECK(event_type IN ('WagerTransactionProcessed','WagerTransactionRejected','WalletBalanceChanged','WagerTransactionPendingReference')),
 payload jsonb NOT NULL,
 occurred_at timestamptz NOT NULL,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_token uuid,
 lease_until timestamptz,
 published_at timestamptz,
 CHECK ((lease_token IS NULL)=(lease_until IS NULL))
);
CREATE INDEX unpublished_events ON outbox(next_attempt_at,occurred_at) WHERE published_at IS NULL;

CREATE FUNCTION forbid_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'immutable audit record' USING ERRCODE='23514'; END $$;
CREATE TRIGGER ledger_immutable BEFORE UPDATE OR DELETE ON wallet_ledger FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER ledger_no_truncate BEFORE TRUNCATE ON wallet_ledger FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER transactions_no_delete BEFORE DELETE ON wager_transactions FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE FUNCTION guard_transaction() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status IN ('PROCESSED','REJECTED','FAILED') THEN RAISE EXCEPTION 'terminal transaction is immutable' USING ERRCODE='23514'; END IF;
 IF (to_jsonb(NEW)-ARRAY['status','failure_code','result_balance','resolved_reference_id','attempts','next_attempt_at','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['status','failure_code','result_balance','resolved_reference_id','attempts','next_attempt_at','updated_at']) THEN
 RAISE EXCEPTION 'transaction business fields are immutable' USING ERRCODE='23514'; END IF;
 IF NEW.status='PENDING' THEN RAISE EXCEPTION 'invalid transition' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER transaction_transition BEFORE UPDATE ON wager_transactions FOR EACH ROW EXECUTE FUNCTION guard_transaction();
CREATE FUNCTION guard_outbox() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.event_id,NEW.aggregate_id,NEW.event_type,NEW.payload,NEW.occurred_at) IS DISTINCT FROM (OLD.event_id,OLD.aggregate_id,OLD.event_type,OLD.payload,OLD.occurred_at) THEN
 RAISE EXCEPTION 'outbox snapshot is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER outbox_snapshot BEFORE UPDATE ON outbox FOR EACH ROW EXECUTE FUNCTION guard_outbox();

-- Deferred checks run at commit, after balance, transaction and ledger are written.
CREATE FUNCTION check_wallet_integrity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE wid uuid; current_wallet wallets%ROWTYPE; reconstructed numeric; latest_version bigint;
BEGIN
 IF TG_TABLE_NAME='wallets' THEN wid:=NEW.id; ELSE wid:=NEW.wallet_id; END IF;
 SELECT * INTO current_wallet FROM wallets WHERE id=wid;
 SELECT COALESCE(sum(CASE WHEN direction='CREDIT' THEN amount::numeric ELSE -amount::numeric END),0), COALESCE(max(wallet_version),1)
 INTO reconstructed,latest_version FROM wallet_ledger WHERE wallet_id=wid;
 IF current_wallet.balance::numeric<>reconstructed OR current_wallet.version<>latest_version THEN
 RAISE EXCEPTION 'wallet and ledger disagree' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM (
 SELECT balance_before,COALESCE(lag(balance_after) OVER (ORDER BY wallet_version),0) expected,
 wallet_version, lag(wallet_version) OVER (ORDER BY wallet_version) previous_version
 FROM wallet_ledger WHERE wallet_id=wid) s
 WHERE balance_before<>expected OR (previous_version IS NOT NULL AND wallet_version<>previous_version+1)) THEN
 RAISE EXCEPTION 'broken ledger chain' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER wallet_integrity AFTER INSERT OR UPDATE ON wallets DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_wallet_integrity();
CREATE CONSTRAINT TRIGGER ledger_integrity AFTER INSERT ON wallet_ledger DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_wallet_integrity();

CREATE FUNCTION check_transaction_integrity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE tid uuid; t wager_transactions%ROWTYPE; r wager_transactions%ROWTYPE; l wallet_ledger%ROWTYPE; expected_direction text;
BEGIN
 IF TG_TABLE_NAME='wallet_ledger' THEN tid:=NEW.transaction_id; ELSE tid:=NEW.id; END IF;
 SELECT * INTO t FROM wager_transactions WHERE id=tid;
 SELECT * INTO l FROM wallet_ledger WHERE transaction_id=tid;
 IF t.status='PROCESSED' AND t.kind<>'LOSS' THEN
  IF l.id IS NULL OR l.wallet_id<>t.wallet_id OR l.amount<>t.amount OR l.currency<>t.currency OR l.balance_after<>t.result_balance THEN
  RAISE EXCEPTION 'processed transaction requires matching ledger' USING ERRCODE='23514'; END IF;
  expected_direction:=CASE WHEN t.kind='BET' THEN 'DEBIT' ELSE 'CREDIT' END;
  IF t.kind IN ('REFUND','ROLLBACK') THEN
   SELECT * INTO r FROM wager_transactions WHERE id=t.resolved_reference_id;
   IF r.id IS NULL OR r.status<>'PROCESSED' OR (r.provider_id,r.player_id,r.wallet_id,r.currency,r.round_id,r.amount,r.external_id) IS DISTINCT FROM (t.provider_id,t.player_id,t.wallet_id,t.currency,t.round_id,t.amount,t.reference_external_id) THEN
   RAISE EXCEPTION 'invalid reversal reference' USING ERRCODE='23514'; END IF;
   IF (t.kind='REFUND' AND r.kind<>'BET') OR (t.kind='ROLLBACK' AND r.kind NOT IN ('BET','WIN','REFUND')) THEN
   RAISE EXCEPTION 'invalid reversal kind' USING ERRCODE='23514'; END IF;
   IF t.kind='ROLLBACK' AND r.kind IN ('WIN','REFUND') THEN expected_direction:='DEBIT'; END IF;
  END IF;
  IF l.direction<>expected_direction THEN RAISE EXCEPTION 'incorrect ledger direction' USING ERRCODE='23514'; END IF;
 ELSIF l.id IS NOT NULL THEN RAISE EXCEPTION 'nonfinancial transaction has ledger' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER transaction_integrity AFTER INSERT OR UPDATE ON wager_transactions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_transaction_integrity();
CREATE CONSTRAINT TRIGGER ledger_transaction_integrity AFTER INSERT ON wallet_ledger DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_transaction_integrity();
GRANT SELECT,INSERT,UPDATE ON wallets,wager_transactions,outbox TO jungle_app;
GRANT SELECT,INSERT ON wallet_ledger,inbox TO jungle_app;
REVOKE UPDATE,DELETE,TRUNCATE ON wallet_ledger FROM jungle_app;
