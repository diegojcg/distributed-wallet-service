package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-wallet-service/internal/domain"
	"jungle-wallet-service/internal/failpoint"
	"jungle-wallet-service/internal/requestmeta"
	"time"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("idempotency conflict")
	ErrOwnership = errors.New("wallet ownership mismatch")
)

type Observer func(context.Context, Result, error, time.Duration)
type Store struct {
	db      *pgxpool.Pool
	observe Observer
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db, observe: func(context.Context, Result, error, time.Duration) {}}
}

type WalletView struct {
	ID       string       `json:"id"`
	PlayerID string       `json:"playerId"`
	Balance  domain.Money `json:"balance"`
	Version  int64        `json:"version"`
}
type Result struct {
	TransactionID string        `json:"transactionId"`
	Status        domain.Status `json:"status"`
	Balance       *domain.Money `json:"balance,omitempty"`
	FailureCode   string        `json:"failureCode,omitempty"`
	Replay        bool          `json:"idempotentReplay"`
}

func conflict(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		return ErrConflict
	}
	return err
}
func scanWallet(row pgx.Row) (domain.Wallet, error) {
	var id, player, currency string
	var minor, version int64
	var created, updated time.Time
	err := row.Scan(&id, &player, &currency, &minor, &version, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Wallet{}, ErrNotFound
	}
	if err != nil {
		return domain.Wallet{}, err
	}
	money, err := domain.FromMinor(minor, currency)
	if err != nil {
		return domain.Wallet{}, err
	}
	return domain.RestoreWallet(id, player, money, version, created, updated)
}

const walletCols = "id,player_id,currency,balance,version,created_at,updated_at"

func view(w domain.Wallet) WalletView {
	return WalletView{w.ID(), w.PlayerID(), w.Balance(), w.Version()}
}
func (s *Store) Wallet(ctx context.Context, id string) (WalletView, error) {
	w, e := scanWallet(s.db.QueryRow(ctx, "SELECT "+walletCols+" FROM wallets WHERE id=$1", id))
	return view(w), e
}

func NewObserved(db *pgxpool.Pool, observe Observer) *Store { return &Store{db: db, observe: observe} }
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func metadata(ctx context.Context, fallback string) requestmeta.Metadata {
	m := requestmeta.From(ctx)
	if m.CorrelationID == "" {
		m.CorrelationID = fallback
	}
	return m
}
func (s *Store) Open(ctx context.Context, player string, balance domain.Money) (WalletView, error) {
	now := time.Now().UTC()
	w, err := domain.NewWallet(uuid.NewString(), player, balance, now)
	if err != nil {
		return WalletView{}, err
	}
	if balance.Currency() != "BRL" {
		return WalletView{}, domain.ErrCurrency
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return WalletView{}, err
	}
	defer rollback(tx)
	now, err = databaseTime(ctx, tx)
	if err != nil {
		return WalletView{}, err
	}
	w, err = domain.NewWallet(w.ID(), player, balance, now)
	if err != nil {
		return WalletView{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO wallets(id,player_id,currency,balance,version,created_at,updated_at) VALUES($1,$2,$3,$4,1,$5,$5)`, w.ID(), w.PlayerID(), balance.Currency(), balance.Minor(), now)
	if err != nil {
		return WalletView{}, conflict(err)
	}
	if balance.Minor() > 0 {
		id := uuid.NewString()
		m := metadata(ctx, id)
		t, e := domain.NewOpeningTransaction(id, w, m.CorrelationID, m.CausationID, now)
		if e != nil {
			return WalletView{}, e
		}
		if _, err = insertTransaction(ctx, tx, t); err != nil {
			return WalletView{}, err
		}
		zero, _ := domain.Zero(balance.Currency())
		l, e := domain.NewWalletLedgerEntry(uuid.NewString(), id, w, domain.Credit, balance, zero, now)
		if e != nil {
			return WalletView{}, e
		}
		if err = insertLedger(ctx, tx, l); err != nil {
			return WalletView{}, err
		}
		if err = financialEvents(ctx, tx, t, &l); err != nil {
			return WalletView{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return WalletView{}, err
	}
	return view(w), nil
}
func insertLedger(ctx context.Context, tx pgx.Tx, entry domain.WalletLedgerEntry) error {
	l := entry.Snapshot()
	_, err := tx.Exec(ctx, `INSERT INTO wallet_ledger(id,wallet_id,transaction_id,direction,amount,currency,balance_before,balance_after,wallet_version,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, l.ID, l.WalletID, l.TransactionID, l.Direction, l.Money.Minor(), l.Money.Currency(), l.Before.Minor(), l.After.Minor(), l.WalletVersion, l.CreatedAt)
	return err
}
func insertEvent(ctx context.Context, tx pgx.Tx, e domain.Event, err error) error {
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox(event_id,aggregate_id,event_type,payload,occurred_at) VALUES($1,$2,$3,$4,$5)`, e.ID(), e.AggregateID(), e.Type(), e.JSON(), e.OccurredAt())
	return err
}
func financialEvents(ctx context.Context, tx pgx.Tx, t domain.WagerTransaction, l *domain.WalletLedgerEntry) error {
	e, err := domain.NewTransactionProcessedEvent(uuid.NewString(), t)
	if err = insertEvent(ctx, tx, e, err); err != nil {
		return err
	}
	if l == nil {
		return nil
	}
	e, err = domain.NewWalletBalanceChangedEvent(uuid.NewString(), t, *l)
	return insertEvent(ctx, tx, e, err)
}
func result(t domain.WagerTransaction, replay bool) Result {
	s := t.Snapshot()
	r := Result{TransactionID: s.ID, Status: s.Status, FailureCode: s.FailureCode, Replay: replay}
	if s.HasResult {
		m := s.Result
		r.Balance = &m
	}
	return r
}

const transactionCols = `id,wallet_id,player_id,currency,kind,amount,COALESCE(provider_id,''),COALESCE(external_id,''),COALESCE(idempotency_key,''),COALESCE(payload_hash,''),COALESCE(round_id,''),COALESCE(game_id,''),COALESCE(reference_external_id,''),status,COALESCE(failure_code,''),result_balance,COALESCE(resolved_reference_id::text,''),attempts,next_attempt_at,created_at,updated_at,correlation_id,COALESCE(causation_id,'')`

func scanTransaction(row pgx.Row) (domain.WagerTransaction, error) {
	var s domain.TransactionSnapshot
	var amount int64
	var currency string
	var balance *int64
	var next *time.Time
	o := &s.Operation
	err := row.Scan(&s.ID, &o.WalletID, &o.PlayerID, &currency, &o.Kind, &amount, &o.ProviderID, &o.ExternalID, &s.Key, &s.Hash, &o.RoundID, &o.GameID, &o.Reference, &s.Status, &s.FailureCode, &balance, &s.ResolvedReferenceID, &s.Attempts, &next, &s.CreatedAt, &s.UpdatedAt, &s.CorrelationID, &s.CausationID)
	if err != nil {
		return domain.WagerTransaction{}, err
	}
	o.Money, err = domain.FromMinor(amount, currency)
	if err != nil {
		return domain.WagerTransaction{}, err
	}
	if balance != nil {
		s.Result, err = domain.FromMinor(*balance, currency)
		if err != nil {
			return domain.WagerTransaction{}, err
		}
		s.HasResult = true
	}
	if next != nil {
		s.NextAttemptAt = *next
	}
	return domain.RestoreWagerTransaction(s)
}
func insertTransaction(ctx context.Context, tx pgx.Tx, t domain.WagerTransaction) (bool, error) {
	s := t.Snapshot()
	o := s.Operation
	var balance any
	if s.HasResult {
		balance = s.Result.Minor()
	}
	tag, err := tx.Exec(ctx, `INSERT INTO wager_transactions(id,wallet_id,player_id,currency,kind,amount,provider_id,external_id,idempotency_key,payload_hash,round_id,game_id,reference_external_id,status,result_balance,created_at,updated_at,correlation_id,causation_id) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),NULLIF($13,''),$14,$15,$16,$17,$18,NULLIF($19,'')) ON CONFLICT DO NOTHING`, s.ID, o.WalletID, o.PlayerID, o.Money.Currency(), o.Kind, o.Money.Minor(), o.ProviderID, o.ExternalID, s.Key, s.Hash, o.RoundID, o.GameID, o.Reference, s.Status, balance, s.CreatedAt, s.UpdatedAt, s.CorrelationID, s.CausationID)
	return tag.RowsAffected() == 1, err
}
func updateTransaction(ctx context.Context, tx pgx.Tx, t domain.WagerTransaction) error {
	s := t.Snapshot()
	var next any
	if !s.NextAttemptAt.IsZero() {
		next = s.NextAttemptAt
	}
	var balance any
	if s.HasResult {
		balance = s.Result.Minor()
	}
	_, err := tx.Exec(ctx, `UPDATE wager_transactions SET status=$2,failure_code=NULLIF($3,''),result_balance=$4,resolved_reference_id=NULLIF($5,'')::uuid,next_attempt_at=$6,attempts=$7,updated_at=$8 WHERE id=$1`, s.ID, s.Status, s.FailureCode, balance, s.ResolvedReferenceID, next, s.Attempts, s.UpdatedAt)
	return err
}
func replay(ctx context.Context, tx pgx.Tx, o domain.Operation, key, hash string) (Result, bool, error) {
	rows, err := tx.Query(ctx, `SELECT `+transactionCols+` FROM wager_transactions WHERE provider_id=$1 AND (idempotency_key=$2 OR external_id=$3)`, o.ProviderID, key, o.ExternalID)
	if err != nil {
		return Result{}, false, err
	}
	defer rows.Close()
	var r Result
	count := 0
	for rows.Next() {
		t, e := scanTransaction(rows)
		if e != nil {
			return Result{}, false, e
		}
		count++
		if t.Snapshot().Hash != hash || count > 1 {
			return Result{}, false, ErrConflict
		}
		r = result(t, true)
	}
	return r, count == 1, rows.Err()
}
func (s *Store) Apply(ctx context.Context, o domain.Operation, key string) (r Result, err error) {
	started := time.Now()
	defer func() { s.observe(ctx, r, err, time.Since(started)) }()
	r, err = s.run(ctx, o, key, nil, false)
	if err != nil && permanent(err) {
		r, err = s.auditFailure(ctx, o, key, nil)
	}
	if err == nil {
		failpoint.Hit("after_http_commit")
	}
	return
}
func (s *Store) run(ctx context.Context, o domain.Operation, key string, inbox *Message, resume bool) (Result, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Result{}, err
	}
	defer rollback(tx)
	if inbox != nil {
		if err = registerInbox(ctx, tx, *inbox); err != nil {
			return Result{}, err
		}
	}
	r, err := s.apply(ctx, tx, o, key, resume)
	if err != nil {
		return Result{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return r, nil
}
func (s *Store) apply(ctx context.Context, tx pgx.Tx, o domain.Operation, key string, resume bool) (Result, error) {
	id := uuid.NewString()
	now := time.Now().UTC()
	m := metadata(ctx, id)
	t, err := domain.NewWagerTransaction(id, o, key, m.CorrelationID, m.CausationID, now)
	if err != nil {
		return Result{}, err
	}
	o = t.Operation()
	if !resume {
		if r, found, e := replay(ctx, tx, o, key, t.Snapshot().Hash); e != nil || found {
			return r, e
		}
	}
	w, err := scanWallet(tx.QueryRow(ctx, "SELECT "+walletCols+" FROM wallets WHERE id=$1 FOR UPDATE", o.WalletID))
	if err != nil {
		return Result{}, err
	}
	if w.PlayerID() != o.PlayerID {
		return Result{}, ErrOwnership
	}
	// Scheduling uses database wall time; transitions additionally preserve stored timestamps.
	now, err = databaseTime(ctx, tx)
	if err != nil {
		return Result{}, err
	}
	if resume {
		t, err = scanTransaction(tx.QueryRow(ctx, "SELECT "+transactionCols+" FROM wager_transactions WHERE provider_id=$1 AND external_id=$2 FOR UPDATE", o.ProviderID, o.ExternalID))
		if err != nil {
			return Result{}, err
		}
		if t.Status() != domain.PendingReference || t.Snapshot().NextAttemptAt.After(now) {
			return result(t, true), nil
		}
	} else {
		// The provisional constructor above only validates input/hash. Never persist its host clock.
		t, err = domain.NewWagerTransaction(id, o, key, m.CorrelationID, m.CausationID, notBefore(now, w.UpdatedAt()))
		if err != nil {
			return Result{}, err
		}
		inserted, e := insertTransaction(ctx, tx, t)
		if e != nil {
			return Result{}, e
		}
		if !inserted {
			r, found, e := replay(ctx, tx, o, key, t.Snapshot().Hash)
			if e != nil {
				return Result{}, e
			}
			if !found {
				return Result{}, fmt.Errorf("conflicting transaction disappeared")
			}
			return r, nil
		}
	}
	var reference *domain.WagerTransaction
	reversed := false
	if o.Reference != "" {
		ref, e := scanTransaction(tx.QueryRow(ctx, "SELECT "+transactionCols+" FROM wager_transactions WHERE provider_id=$1 AND external_id=$2", o.ProviderID, o.Reference))
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return Result{}, e
		}
		if e == nil {
			reference = &ref
			if o.Kind == domain.Refund || o.Kind == domain.Rollback {
				if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wager_transactions WHERE resolved_reference_id=$1 AND status='PROCESSED' AND kind IN ('REFUND','ROLLBACK'))`, ref.ID()).Scan(&reversed); err != nil {
					return Result{}, err
				}
			}
		}
	}
	now = notBefore(now, w.UpdatedAt(), t.Snapshot().UpdatedAt)
	after, direction, err := t.Evaluate(w, reference, reversed, now)
	if err != nil {
		return Result{}, err
	}
	if err = updateTransaction(ctx, tx, t); err != nil {
		return Result{}, err
	}
	switch t.Status() {
	case domain.Processed:
		var entry *domain.WalletLedgerEntry
		if after.Version() != w.Version() {
			tag, e := tx.Exec(ctx, `UPDATE wallets SET balance=$2,version=$3,updated_at=$4 WHERE id=$1 AND version=$5`, after.ID(), after.Balance().Minor(), after.Version(), after.UpdatedAt(), w.Version())
			if e != nil {
				return Result{}, e
			}
			if tag.RowsAffected() != 1 {
				return Result{}, fmt.Errorf("wallet version changed under lock")
			}
			l, e := domain.NewWalletLedgerEntry(uuid.NewString(), t.ID(), after, direction, o.Money, w.Balance(), now)
			if e != nil {
				return Result{}, e
			}
			if err = insertLedger(ctx, tx, l); err != nil {
				return Result{}, err
			}
			entry = &l
		}
		err = financialEvents(ctx, tx, t, entry)
	case domain.Rejected:
		e, x := domain.NewTransactionRejectedEvent(uuid.NewString(), t)
		err = insertEvent(ctx, tx, e, x)
	case domain.PendingReference:
		if !resume {
			e, x := domain.NewTransactionPendingReferenceEvent(uuid.NewString(), t)
			err = insertEvent(ctx, tx, e, x)
		}
	}
	if err != nil {
		return Result{}, err
	}
	return result(t, false), nil
}

type Reconciliation struct {
	WalletID   string       `json:"walletId"`
	Stored     domain.Money `json:"storedBalance"`
	Calculated domain.Money `json:"calculatedBalance"`
	Difference domain.Money `json:"difference"`
	Consistent bool         `json:"consistent"`
	Checked    int64        `json:"checkedEntries"`
}

func (s *Store) Reconcile(ctx context.Context, id string) (Reconciliation, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Reconciliation{}, err
	}
	defer rollback(tx)
	w, err := scanWallet(tx.QueryRow(ctx, "SELECT "+walletCols+" FROM wallets WHERE id=$1", id))
	if err != nil {
		return Reconciliation{}, err
	}
	var sum string
	var count int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(sum(CASE WHEN direction='CREDIT' THEN amount::numeric ELSE -amount::numeric END),0)::text,count(*) FROM wallet_ledger WHERE wallet_id=$1`, id).Scan(&sum, &count)
	if err != nil {
		return Reconciliation{}, err
	}
	// The database invariant guarantees the aggregate fits a nonnegative int64.
	var minor int64
	if _, err = fmt.Sscan(sum, &minor); err != nil {
		return Reconciliation{}, err
	}
	calculated, _ := domain.FromMinor(minor, w.Balance().Currency())
	difference, err := w.Balance().Sub(calculated)
	if err != nil {
		return Reconciliation{}, err
	}
	return Reconciliation{id, w.Balance(), calculated, difference, difference.Minor() == 0, count}, tx.Commit(ctx)
}
func (s *Store) Transaction(ctx context.Context, provider, id string, external bool) (Result, error) {
	query := `SELECT id,status,result_balance,currency,COALESCE(failure_code,'') FROM wager_transactions WHERE provider_id=$1 AND id=$2`
	if external {
		query = `SELECT id,status,result_balance,currency,COALESCE(failure_code,'') FROM wager_transactions WHERE provider_id=$1 AND external_id=$2`
	}
	var r Result
	var minor *int64
	var currency string
	err := s.db.QueryRow(ctx, query, provider, id).Scan(&r.TransactionID, &r.Status, &minor, &currency, &r.FailureCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if minor != nil {
		m, _ := domain.FromMinor(*minor, currency)
		r.Balance = &m
	}
	return r, nil
}
