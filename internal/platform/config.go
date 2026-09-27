package platform

import (
	"fmt"
	"net/url"
	"os"
)

type Config struct{ HTTPAddr, DatabaseURL, Issuer, JWKSURL, Audience, SQSEndpoint, CredentialsFile string }

func ReadConfig() (Config, error) {
	c := Config{getenv("HTTP_ADDR", "127.0.0.1:58000"), os.Getenv("DATABASE_URL"), os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_JWKS_URL"), getenv("OIDC_AUDIENCE", "jungle-wallet"), os.Getenv("SQS_ENDPOINT"), os.Getenv("BROKER_CREDENTIALS_FILE")}
	for name, value := range map[string]string{"DATABASE_URL": c.DatabaseURL, "OIDC_ISSUER": c.Issuer, "OIDC_JWKS_URL": c.JWKSURL, "SQS_ENDPOINT": c.SQSEndpoint} {
		u, e := url.Parse(value)
		if e != nil || u.Host == "" || u.Scheme == "" {
			return c, fmt.Errorf("%s must be an absolute URL", name)
		}
	}
	if c.CredentialsFile == "" {
		return c, fmt.Errorf("BROKER_CREDENTIALS_FILE is required")
	}
	return c, nil
}
func getenv(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}
