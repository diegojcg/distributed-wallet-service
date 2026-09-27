package platform

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"jungle-wallet-service/internal/requestmeta"
	"jungle-wallet-service/internal/store"
	"strconv"
	"time"
)

type Metrics struct {
	Results                                     *prometheus.CounterVec
	Replays, Divergences                        prometheus.Counter
	Latency                                     prometheus.Histogram
	Retries, Conflicts                          *prometheus.CounterVec
	DLQ                                         *prometheus.GaugeVec
	OutboxAge, OutboxPending, ReferencesPending prometheus.Gauge
}

func NewMetrics(r *prometheus.Registry) *Metrics {
	m := &Metrics{
		Results:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wallet_transactions_total", Help: "Operation responses (including replays) by durable state and transport."}, []string{"status", "transport"}),
		Replays:           prometheus.NewCounter(prometheus.CounterOpts{Name: "wallet_idempotent_replays_total", Help: "HTTP/SQS operation replays."}),
		Divergences:       prometheus.NewCounter(prometheus.CounterOpts{Name: "wallet_reconciliation_divergences_total", Help: "Inconsistent reconciliations."}),
		Latency:           prometheus.NewHistogram(prometheus.HistogramOpts{Name: "wallet_processing_seconds", Help: "Store processing latency for HTTP, SQS and reference workers."}),
		Retries:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wallet_retries_total", Help: "Failed or repeated processing attempts by component."}, []string{"component"}),
		Conflicts:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wallet_concurrency_conflicts_total", Help: "Idempotency conflicts, database deadlocks and serialization failures."}, []string{"reason"}),
		DLQ:               prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "wallet_dlq_messages", Help: "Approximate visible plus in-flight DLQ messages; shared broker state, do not sum replicas."}, []string{"provider"}),
		OutboxAge:         prometheus.NewGauge(prometheus.GaugeOpts{Name: "wallet_outbox_oldest_seconds", Help: "Oldest unpublished event age; shared DB state, do not sum replicas."}),
		OutboxPending:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "wallet_outbox_pending", Help: "Unpublished outbox events; shared DB state, do not sum replicas."}),
		ReferencesPending: prometheus.NewGauge(prometheus.GaugeOpts{Name: "wallet_pending_references", Help: "Durable reference dependencies; shared DB state, do not sum replicas."}),
	}
	for _, status := range []string{"PROCESSED", "REJECTED", "PENDING_REFERENCE", "FAILED"} {
		for _, transport := range []string{"http", "sqs", "reference"} {
			m.Results.WithLabelValues(status, transport)
		}
	}
	for _, c := range []string{"outbox", "reference", "sqs_receive", "sqs_processing", "metrics"} {
		m.Retries.WithLabelValues(c)
	}
	for _, c := range []string{"idempotency", "deadlock", "serialization"} {
		m.Conflicts.WithLabelValues(c)
	}
	r.MustRegister(m.Results, m.Replays, m.Divergences, m.Latency, m.Retries, m.Conflicts, m.DLQ, m.OutboxAge, m.OutboxPending, m.ReferencesPending)
	return m
}
func (m *Metrics) Observe(ctx context.Context, r store.Result, err error, elapsed time.Duration) {
	transport := requestmeta.From(ctx).Transport
	if transport == "" {
		transport = "reference"
	}
	m.Latency.Observe(elapsed.Seconds())
	if err == nil && r.TransactionID != "" {
		if transport != "reference" || !r.Replay {
			m.Results.WithLabelValues(string(r.Status), transport).Inc()
		}
		if r.Replay && transport != "reference" {
			m.Replays.Inc()
		}
	}
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrMessageConflict) {
		m.Conflicts.WithLabelValues("idempotency").Inc()
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "40P01":
			m.Conflicts.WithLabelValues("deadlock").Inc()
		case "40001":
			m.Conflicts.WithLabelValues("serialization").Inc()
		}
	}
	if transport == "reference" && !r.Replay {
		m.Retries.WithLabelValues("reference").Inc()
	}
}
func (m *Metrics) Refresh(ctx context.Context, s *store.Store, b *Broker) {
	pending, age, refs, err := s.Statistics(ctx)
	if err == nil {
		m.OutboxPending.Set(float64(pending))
		m.OutboxAge.Set(age)
		m.ReferencesPending.Set(float64(refs))
	} else {
		m.Retries.WithLabelValues("metrics").Inc()
	}
	for _, q := range b.DLQs {
		r, e := b.Client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: &q.URL, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
		if e != nil {
			m.Retries.WithLabelValues("metrics").Inc()
			continue
		}
		visible, _ := strconv.ParseFloat(r.Attributes["ApproximateNumberOfMessages"], 64)
		inflight, _ := strconv.ParseFloat(r.Attributes["ApproximateNumberOfMessagesNotVisible"], 64)
		m.DLQ.WithLabelValues(q.ProviderID).Set(visible + inflight)
	}
}
