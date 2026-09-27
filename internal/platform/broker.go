package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/fx"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type InputQueue struct{ URL, ProviderID string }
type Broker struct {
	Client    *sqs.Client
	Inputs    []InputQueue
	DLQs      []InputQueue
	EventsURL string
}

func NewBroker(lc fx.Lifecycle, c Config) (*Broker, error) {
	data, err := os.ReadFile(c.CredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("read broker credentials: %w", err)
	}
	var creds struct {
		AccessKey string            `json:"accessKeyId"`
		Secret    string            `json:"secretAccessKey"`
		Queues    map[string]string `json:"queueProviders"`
	}
	if err = json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	if creds.AccessKey == "" || creds.Secret == "" || len(creds.Queues) == 0 {
		return nil, fmt.Errorf("incomplete broker credentials")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	httpClient := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	client := sqs.New(sqs.Options{Region: "us-east-1", BaseEndpoint: aws.String(c.SQSEndpoint), Credentials: credentials.NewStaticCredentialsProvider(creds.AccessKey, creds.Secret, ""), HTTPClient: httpClient, RetryMaxAttempts: 2})
	b := &Broker{Client: client}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		resolve := func(name string) (string, error) {
			result, e := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
			if e != nil {
				return "", fmt.Errorf("resolve queue %s: %w", name, e)
			}
			u, e := url.Parse(*result.QueueUrl)
			if e != nil {
				return "", e
			}
			endpoint, _ := url.Parse(c.SQSEndpoint)
			u.Scheme = endpoint.Scheme
			u.Host = endpoint.Host
			return u.String(), nil
		}
		for name, provider := range creds.Queues {
			u, e := resolve(name)
			if e != nil {
				return e
			}
			b.Inputs = append(b.Inputs, InputQueue{u, provider})
			dlq, e := resolve(strings.TrimSuffix(name, ".fifo") + "-dlq.fifo")
			if e != nil {
				return e
			}
			b.DLQs = append(b.DLQs, InputQueue{dlq, provider})
		}
		var e error
		b.EventsURL, e = resolve("wallet-events.fifo")
		if e != nil {
			return e
		}
		return b.Ready(ctx)
	}, OnStop: func(context.Context) error { transport.CloseIdleConnections(); return nil }})
	return b, nil
}
func (b *Broker) Ready(ctx context.Context) error {
	for _, q := range b.Inputs {
		_, err := b.Client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: &q.URL, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
		if err != nil {
			return err
		}
	}
	return nil
}
