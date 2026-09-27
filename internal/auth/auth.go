package auth

import (
	"context"
	"errors"
	"github.com/coreos/go-oidc/v3/oidc"
	"net/http"
	"strings"
	"time"
)

type Identity struct {
	ProviderID string `json:"provider_id"`
	Role       string `json:"service_role"`
}
type Verifier struct {
	verifier *oidc.IDTokenVerifier
	client   *http.Client
	jwks     string
}

func New(issuer, jwks, audience string) *Verifier {
	client := &http.Client{Timeout: 5 * time.Second, Transport: http.DefaultTransport.(*http.Transport).Clone()}
	ctx := oidc.ClientContext(context.Background(), client)
	keys := oidc.NewRemoteKeySet(ctx, jwks)
	return &Verifier{verifier: oidc.NewVerifier(issuer, keys, &oidc.Config{ClientID: audience, SupportedSigningAlgs: []string{"RS256"}}), client: client, jwks: jwks}
}
func (v *Verifier) Authenticate(ctx context.Context, header string) (Identity, error) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return Identity{}, errors.New("missing bearer token")
	}
	token, err := v.verifier.Verify(ctx, parts[1])
	if err != nil {
		return Identity{}, errors.New("invalid bearer token")
	}
	var identity Identity
	if err = token.Claims(&identity); err != nil {
		return Identity{}, err
	}
	if identity.Role == "provider" && identity.ProviderID == "" {
		return Identity{}, errors.New("missing provider identity")
	}
	return identity, nil
}

func (v *Verifier) Close() { v.client.CloseIdleConnections() }
func (v *Verifier) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwks, nil)
	if err != nil {
		return err
	}
	res, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New("JWKS unavailable")
	}
	return nil
}
