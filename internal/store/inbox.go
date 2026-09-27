package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"jungle-wallet-service/internal/domain"
	"time"
)

var ErrMessageConflict = errors.New("message identity reused with different payload")

type Message struct{ Consumer, ID, Hash string }

func registerInbox(ctx context.Context, tx pgx.Tx, m Message) error {
	tag, err := tx.Exec(ctx, `INSERT INTO inbox(consumer_name,message_id,payload_hash,received_at,completed_at) VALUES($1,$2,$3,now(),clock_timestamp()) ON CONFLICT DO NOTHING`, m.Consumer, m.ID, m.Hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var hash string
		if err = tx.QueryRow(ctx, "SELECT payload_hash FROM inbox WHERE consumer_name=$1 AND message_id=$2", m.Consumer, m.ID).Scan(&hash); err != nil {
			return err
		}
		if hash != m.Hash {
			return ErrMessageConflict
		}
	}
	return nil
}
func (s *Store) Consume(ctx context.Context, o domain.Operation, key, consumer, id, hash string) (r Result, err error) {
	started := time.Now()
	defer func() { s.observe(ctx, r, err, time.Since(started)) }()
	m := &Message{consumer, id, hash}
	r, err = s.run(ctx, o, key, m, false)
	if err != nil && permanent(err) {
		r, err = s.auditFailure(ctx, o, key, m)
	}
	return
}
