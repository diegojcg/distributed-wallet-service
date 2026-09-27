package requestmeta

import "context"

type Metadata struct{ CorrelationID, CausationID, WalletID, ProviderID, TransactionID, Transport string }
type key struct{}

func With(ctx context.Context, m Metadata) context.Context { return context.WithValue(ctx, key{}, m) }
func From(ctx context.Context) Metadata                    { m, _ := ctx.Value(key{}).(Metadata); return m }

func ValidID(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, r := range s {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
