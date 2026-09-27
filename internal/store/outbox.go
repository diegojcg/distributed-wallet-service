package store

import (
	"context"
	"github.com/google/uuid"
)

type Publication struct {
	ID, AggregateID, Lease string
	Payload                []byte
	Attempts               int
}

func (s *Store) Claim(ctx context.Context) ([]Publication, error) {
	token := uuid.NewString()
	rows, err := s.db.Query(ctx, `WITH selected AS (SELECT event_id FROM outbox WHERE published_at IS NULL AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY occurred_at,event_id LIMIT 10 FOR UPDATE SKIP LOCKED) UPDATE outbox SET lease_token=$1,lease_until=now()+interval '30 seconds',attempts=attempts+1 FROM selected WHERE outbox.event_id=selected.event_id RETURNING outbox.event_id,aggregate_id,payload,attempts`, token)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Publication
	for rows.Next() {
		p := Publication{Lease: token}
		if err = rows.Scan(&p.ID, &p.AggregateID, &p.Payload, &p.Attempts); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
func (s *Store) Published(ctx context.Context, p Publication) error {
	_, err := s.db.Exec(ctx, `UPDATE outbox SET published_at=now(),lease_token=NULL,lease_until=NULL WHERE event_id=$1 AND lease_token=$2`, p.ID, p.Lease)
	return err
}
func (s *Store) RetryPublication(ctx context.Context, p Publication) error {
	attempt := min(p.Attempts, 5)
	delay := 1 << attempt
	_, err := s.db.Exec(ctx, `UPDATE outbox SET lease_token=NULL,lease_until=NULL,next_attempt_at=clock_timestamp()+$3::int*interval '1 second' WHERE event_id=$1 AND lease_token=$2`, p.ID, p.Lease, delay)
	return err
}
