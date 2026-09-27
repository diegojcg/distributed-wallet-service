package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/fx"
	"io"
	"jungle-wallet-service/internal/domain"
	"jungle-wallet-service/internal/failpoint"
	"jungle-wallet-service/internal/requestmeta"
	"jungle-wallet-service/internal/store"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Workers struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewWorkers(lc fx.Lifecycle, b *Broker, s *store.Store, metrics *Metrics, logger *slog.Logger) *Workers {
	w := &Workers{}
	lc.Append(fx.Hook{OnStart: func(context.Context) error {
		ctx, cancel := context.WithCancel(context.Background())
		w.cancel = cancel
		launch := func(fn func()) { w.wg.Add(1); go func() { defer w.wg.Done(); fn() }() }
		for _, input := range b.Inputs {
			launch(func() { consumeLoop(ctx, b, s, input, metrics, logger) })
		}
		launch(func() {
			periodic(ctx, time.Second, func(ctx context.Context) {
				if err := s.RetryReferences(ctx); err != nil && ctx.Err() == nil {
					metrics.Retries.WithLabelValues("reference").Inc()
					logger.Error("reference worker iteration failed", "errorType", fmtErrorType(err))
				}
			})
		})
		launch(func() {
			periodic(ctx, 500*time.Millisecond, func(ctx context.Context) { publish(ctx, b, s, metrics, logger) })
		})
		launch(func() { periodic(ctx, 3*time.Second, func(ctx context.Context) { metrics.Refresh(ctx, s, b) }) })
		return nil
	}, OnStop: func(ctx context.Context) error {
		w.cancel()
		done := make(chan struct{})
		go func() { w.wg.Wait(); close(done) }()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	return w
}
func periodic(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			iteration, cancel := context.WithTimeout(ctx, 10*time.Second)
			fn(iteration)
			cancel()
		}
	}
}
func publish(ctx context.Context, b *Broker, s *store.Store, metrics *Metrics, logger *slog.Logger) {
	events, err := s.Claim(ctx)
	if err != nil {
		if ctx.Err() == nil {
			metrics.Retries.WithLabelValues("outbox").Inc()
			logger.Error("outbox claim failed", "errorType", fmtErrorType(err))
		}
		return
	}
	for _, event := range events {
		_, err = b.Client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: &b.EventsURL, MessageBody: aws.String(string(event.Payload)), MessageGroupId: &event.AggregateID, MessageDeduplicationId: &event.ID})
		if err != nil {
			if ctx.Err() == nil {
				metrics.Retries.WithLabelValues("outbox").Inc()
				_ = s.RetryPublication(ctx, event)
				logger.Warn("outbox publish will retry", "eventId", event.ID)
			}
			continue
		}
		failpoint.Hit("after_publish")
		if err = s.Published(ctx, event); err != nil && ctx.Err() == nil {
			logger.Warn("outbox confirmation failed", "eventId", event.ID)
		}
	}
}

type RequestData struct {
	domain.Operation
	IdempotencyKey string `json:"idempotencyKey"`
}
type RequestEnvelope struct {
	MessageID     string      `json:"messageId"`
	CorrelationID string      `json:"correlationId,omitempty"`
	Type          string      `json:"type"`
	OccurredAt    time.Time   `json:"occurredAt"`
	Data          RequestData `json:"data"`
}

func decodeMessage(body string, provider string) (RequestEnvelope, string, error) {
	var e RequestEnvelope
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return e, "", err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return e, "", domain.ErrInvalidOperation
	}
	if !requestmeta.ValidID(e.MessageID, 200) || (e.CorrelationID != "" && !requestmeta.ValidID(e.CorrelationID, 128)) || e.Type != "WagerTransactionRequested" || e.OccurredAt.IsZero() || e.Data.ProviderID != provider || e.Data.IdempotencyKey == "" {
		return e, "", domain.ErrInvalidOperation
	}
	if err := e.Data.Operation.Validate(); err != nil {
		return e, "", err
	}
	// Inbox detects any alteration to the envelope bytes; business hashing is separate.
	h := sha256.Sum256([]byte(body))
	return e, hex.EncodeToString(h[:]), nil
}
func consumeLoop(ctx context.Context, b *Broker, s *store.Store, q InputQueue, metrics *Metrics, logger *slog.Logger) {
	for ctx.Err() == nil {
		result, err := b.Client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: &q.URL, MaxNumberOfMessages: 1, WaitTimeSeconds: 10, VisibilityTimeout: 30, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount}})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			metrics.Retries.WithLabelValues("sqs_receive").Inc()
			logger.Warn("SQS receive will retry")
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, msg := range result.Messages {
			handleMessage(ctx, b, s, q, msg, metrics, logger)
		}
	}
}
func handleMessage(ctx context.Context, b *Broker, s *store.Store, q InputQueue, msg types.Message, metrics *Metrics, logger *slog.Logger) {
	work, cancel := context.WithTimeout(ctx, 10*time.Second)
	e, hash, err := decodeMessage(aws.ToString(msg.Body), q.ProviderID)
	if err == nil {
		correlation := e.CorrelationID
		if correlation == "" {
			correlation = e.MessageID
			if len(correlation) > 128 {
				correlation = hash
			}
		}
		work = requestmeta.With(work, requestmeta.Metadata{CorrelationID: correlation, CausationID: e.MessageID, Transport: "sqs"})
		var result store.Result
		result, err = s.Consume(work, e.Data.Operation, e.Data.IdempotencyKey, "wager-consumer", e.MessageID, hash)
		if err == nil {
			logger.Info("SQS transaction result", "correlationId", correlation, "messageId", e.MessageID, "transactionId", result.TransactionID, "walletId", e.Data.WalletID, "providerId", q.ProviderID, "status", result.Status, "failureCode", result.FailureCode)
		}
		if err == nil && result.Status == domain.Failed {
			err = errors.New("durable permanent failure; awaiting redrive")
		}

	}
	if err == nil {
		failpoint.Hit("after_commit_before_ack")
		_, err = b.Client.DeleteMessage(work, &sqs.DeleteMessageInput{QueueUrl: &q.URL, ReceiptHandle: msg.ReceiptHandle})
	}
	cancel()
	if err != nil {
		metrics.Retries.WithLabelValues("sqs_processing").Inc()
		logger.Warn("SQS message not acknowledged", "correlationId", requestmeta.From(work).CorrelationID, "walletId", e.Data.WalletID, "messageId", aws.ToString(msg.MessageId), "providerId", q.ProviderID)
		// Releasing a receipt never implies failure of the financial commit. Inbox handles
		// ambiguous commits and delivery after SIGTERM. Invalid messages reach broker DLQ.
		release, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		attempt, _ := strconv.Atoi(msg.Attributes["ApproximateReceiveCount"])
		visibility := int32(1 << min(max(attempt, 1), 3))
		if ctx.Err() != nil {
			visibility = 0
		}
		_, _ = b.Client.ChangeMessageVisibility(release, &sqs.ChangeMessageVisibilityInput{QueueUrl: &q.URL, ReceiptHandle: msg.ReceiptHandle, VisibilityTimeout: visibility})
	}
}
