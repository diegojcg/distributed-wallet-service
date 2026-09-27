//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func sqsClient(t *testing.T, identity string) *sqs.Client {
	t.Helper()
	key, secret := "jungle-local-root", "jungle-local-root-secret"
	if identity != "root" {
		raw, e := os.ReadFile(filepath.Join(filepath.Dir(os.Getenv("BROKER_CREDENTIALS_FILE")), identity+".json"))
		if e != nil {
			t.Fatal(e)
		}
		var c struct {
			Key    string `json:"accessKeyId"`
			Secret string `json:"secretAccessKey"`
		}
		if e = json.Unmarshal(raw, &c); e != nil {
			t.Fatal(e)
		}
		key, secret = c.Key, c.Secret
	}
	return sqs.New(sqs.Options{Region: "us-east-1", BaseEndpoint: aws.String(os.Getenv("SQS_ENDPOINT")), Credentials: credentials.NewStaticCredentialsProvider(key, secret, ""), RetryMaxAttempts: 1})
}
func queueURL(t *testing.T, c *sqs.Client, name string) string {
	t.Helper()
	r, e := c.GetQueueUrl(context.Background(), &sqs.GetQueueUrlInput{QueueName: &name})
	if e != nil {
		t.Fatal(e)
	}
	return *r.QueueUrl
}
func send(t *testing.T, c *sqs.Client, q, wallet string, body []byte) {
	t.Helper()
	_, e := c.SendMessage(context.Background(), &sqs.SendMessageInput{QueueUrl: &q, MessageBody: aws.String(string(body)), MessageGroupId: &wallet, MessageDeduplicationId: aws.String(uuid.NewString())})
	if e != nil {
		t.Fatal(e)
	}
}
func envelope(op map[string]any, key, msgID string) []byte {
	data := map[string]any{}
	for k, v := range op {
		data[k] = v
	}
	data["idempotencyKey"] = key
	raw, _ := json.Marshal(map[string]any{"messageId": msgID, "type": "WagerTransactionRequested", "occurredAt": time.Now().UTC().Format(time.RFC3339Nano), "data": data})
	return raw
}
func eventually(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached before timeout")
		}
		time.Sleep(150 * time.Millisecond)
	}
}
func testSQS(t *testing.T, h *harness) {
	c := sqsClient(t, "provider-a")
	q := queueURL(t, c, "wager-transactions.fifo")
	wallet, player := h.wallet(t, "100")
	op := operation(wallet, player, uuid.NewString(), "BET", "25")
	key := uuid.NewString()
	call(t, h.urls[0], "POST", "/wagering/transactions", h.provider, key, op, 200)
	var ids []string
	for i := 0; i < 10; i++ {
		id := uuid.NewString()
		ids = append(ids, id)
		send(t, c, q, wallet, envelope(op, key, id))
	}
	eventually(t, 45*time.Second, func() bool {
		var count int
		e := h.db.QueryRow(context.Background(), "SELECT count(*) FROM inbox WHERE message_id=ANY($1)", ids).Scan(&count)
		return e == nil && count == 10
	})
	out := call(t, h.urls[1], "GET", "/wallets/"+wallet, h.internal, "", nil, 200)
	if amount(out) != "75.00" {
		t.Fatal(out)
	}
	var count int
	if e := h.db.QueryRow(context.Background(), "SELECT count(*) FROM wallet_ledger WHERE wallet_id=$1 AND direction='DEBIT'", wallet).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	// Queue authorization is independent of JSON input. Provider A cannot send to B.
	root := sqsClient(t, "root")
	other := queueURL(t, root, "provider-b-wager-transactions.fifo")
	if _, e := c.SendMessage(context.Background(), &sqs.SendMessageInput{QueueUrl: &other, MessageBody: aws.String("{}"), MessageGroupId: &wallet, MessageDeduplicationId: aws.String(uuid.NewString())}); e == nil {
		t.Fatal("cross-provider queue send succeeded")
	}
	// Reusing an inbox identity with modified content must reach the DLQ, never debit.
	mutated := operation(wallet, player, uuid.NewString(), "BET", "1")
	poison := envelope(mutated, uuid.NewString(), ids[0])
	send(t, c, q, wallet, poison)
	dlq := queueURL(t, root, "wager-transactions-dlq.fifo")
	// Include a lost receive response (30s visibility) plus all five deliveries
	// and their backoffs, not only the retry delays observed by live consumers.
	eventually(t, 90*time.Second, func() bool {
		r, e := root.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{QueueUrl: &dlq, MaxNumberOfMessages: 10, WaitTimeSeconds: 1})
		if e != nil {
			return false
		}
		found := false
		for _, m := range r.Messages {
			if aws.ToString(m.Body) == string(poison) {
				found = true
			}
			_, _ = root.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{QueueUrl: &dlq, ReceiptHandle: m.ReceiptHandle})
		}
		return found
	})
	out = call(t, h.urls[2], "GET", "/wallets/"+wallet, h.internal, "", nil, 200)
	if amount(out) != "75.00" {
		t.Fatal(out)
	}
}
func TestHTTPCommitCrash(t *testing.T) {
	h := setup(t, "after_http_commit")
	wallet, player := h.wallet(t, "100")
	op := operation(wallet, player, uuid.NewString(), "BET", "25")
	key := uuid.NewString()
	if _, _, e := request(h.urls[0], "POST", "/wagering/transactions", h.provider, key, op); e == nil {
		t.Fatal("expected connection loss after commit")
	}
	// All children share one marker; only the first process exits at this failpoint.
	replay := call(t, h.urls[1], "POST", "/wagering/transactions", h.provider, key, op, 200)
	if amount(replay) != "75.00" || replay["idempotentReplay"] != true {
		t.Fatal(replay)
	}
}
func TestSQSCommitBeforeACKCrash(t *testing.T) {
	h := setup(t, "after_commit_before_ack")
	wallet, player := h.wallet(t, "100")
	op := operation(wallet, player, uuid.NewString(), "BET", "25")
	key := uuid.NewString()
	id := uuid.NewString()
	c := sqsClient(t, "provider-a")
	q := queueURL(t, c, "wager-transactions.fifo")
	send(t, c, q, wallet, envelope(op, key, id))
	// A ReceiveMessage response can be lost when a prior process stops. Allow one
	// full visibility timeout plus long-poll window before requiring the durable inbox.
	eventually(t, 45*time.Second, func() bool {
		var n int
		e := h.db.QueryRow(context.Background(), "SELECT count(*) FROM inbox WHERE message_id=$1", id).Scan(&n)
		return e == nil && n == 1
	})
	root := sqsClient(t, "root")
	eventually(t, 45*time.Second, func() bool {
		r, e := root.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{QueueUrl: &q, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
		return e == nil && r.Attributes["ApproximateNumberOfMessages"] == "0" && r.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0"
	})
	var balance, count int
	e := h.db.QueryRow(context.Background(), "SELECT balance,(SELECT count(*) FROM wallet_ledger WHERE wallet_id=$1 AND direction='DEBIT') FROM wallets WHERE id=$1", wallet).Scan(&balance, &count)
	if e != nil || balance != 7500 || count != 1 {
		t.Fatal(balance, count, e)
	}
}
func TestPublishBeforeConfirmationCrash(t *testing.T) {
	h := setup(t, "after_publish")
	wallet, _ := h.wallet(t, "100")
	eventually(t, 45*time.Second, func() bool {
		var pending, retried int
		e := h.db.QueryRow(context.Background(), "SELECT count(*) FILTER(WHERE published_at IS NULL),count(*) FILTER(WHERE attempts>=2) FROM outbox WHERE aggregate_id=$1", wallet).Scan(&pending, &retried)
		return e == nil && pending == 0 && retried >= 1
	})
	// Inspect the real output queue with its restricted observer identity. FIFO
	// may suppress the repeated send within five minutes: attempts above proves
	// republication, while the received body must retain the persisted event ID.
	expected := map[string]map[string]any{}
	rows, err := h.db.Query(context.Background(), "SELECT event_id,payload FROM outbox WHERE aggregate_id=$1", wallet)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var payload map[string]any
		if err = json.Unmarshal(raw, &payload); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		expected[id] = payload
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(expected) != 2 {
		t.Fatal(expected, err)
	}
	observer := sqsClient(t, "observer")
	q := queueURL(t, observer, "wallet-events.fifo")
	seen := map[string]bool{}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	eventually(t, 45*time.Second, func() bool {
		out, err := observer.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: &q, MaxNumberOfMessages: 10, WaitTimeSeconds: 1, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll}})
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range out.Messages {
			var payload map[string]any
			if err = json.Unmarshal([]byte(aws.ToString(message.Body)), &payload); err != nil {
				t.Fatal(err)
			}
			id, _ := payload["eventId"].(string)
			if want, ok := expected[id]; ok {
				if !reflect.DeepEqual(want, payload) || message.Attributes["MessageDeduplicationId"] != id || message.Attributes["MessageGroupId"] != wallet {
					t.Fatal("published event differs from durable snapshot", want, payload, message.Attributes)
				}
				seen[id] = true
			}
			if _, err = observer.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: &q, ReceiptHandle: message.ReceiptHandle}); err != nil {
				t.Fatal(err)
			}
		}
		return len(seen) == len(expected)
	})

}
