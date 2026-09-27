package store

import (
	"context"
	"errors"
	"jungle-wallet-service/internal/domain"
	"jungle-wallet-service/internal/requestmeta"
	"time"
)

func (s *Store) RetryReferences(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT id,provider_id,external_id,player_id,wallet_id,round_id,game_id,kind,amount,currency,reference_external_id,idempotency_key,correlation_id,coalesce(causation_id,'') FROM wager_transactions WHERE status='PENDING_REFERENCE' AND next_attempt_at<=now() ORDER BY next_attempt_at LIMIT 10`)
	if err != nil {
		return err
	}
	type task struct {
		o    domain.Operation
		key  string
		meta requestmeta.Metadata
	}
	var tasks []task
	for rows.Next() {
		var t task
		var minor int64
		var currency string
		err = rows.Scan(&t.meta.TransactionID, &t.o.ProviderID, &t.o.ExternalID, &t.o.PlayerID, &t.o.WalletID, &t.o.RoundID, &t.o.GameID, &t.o.Kind, &minor, &currency, &t.o.Reference, &t.key, &t.meta.CorrelationID, &t.meta.CausationID)
		if err != nil {
			rows.Close()
			return err
		}
		t.o.Money, err = domain.FromMinor(minor, currency)
		if err != nil {
			rows.Close()
			return err
		}
		tasks = append(tasks, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, t := range tasks {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		t.meta.Transport = "reference"
		t.meta.WalletID = t.o.WalletID
		t.meta.ProviderID = t.o.ProviderID
		item, cancel := context.WithTimeout(requestmeta.With(ctx, t.meta), 2*time.Second)
		err = s.resume(item, t.o, t.key)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (s *Store) resume(ctx context.Context, o domain.Operation, key string) (err error) {
	started := time.Now()
	var r Result
	defer func() { s.observe(ctx, r, err, time.Since(started)) }()
	r, err = s.run(ctx, o, key, nil, true)
	if err != nil && permanent(err) {
		r, err = s.auditFailure(ctx, o, key, nil)
	}
	return
}
