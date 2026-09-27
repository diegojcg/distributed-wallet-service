#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
export TEST_DATABASE_URL=postgres://jungle_admin:local-admin-only@localhost:${POSTGRES_PORT:-55432}/jungle?sslmode=disable
export DATABASE_URL=postgres://jungle_app:local-app-only@localhost:${POSTGRES_PORT:-55432}/jungle?sslmode=disable
export OIDC_ISSUER=http://localhost:${KEYCLOAK_PORT:-58080}/realms/jungle
export OIDC_JWKS_URL=http://localhost:${KEYCLOAK_PORT:-58080}/realms/jungle/protocol/openid-connect/certs
export OIDC_AUDIENCE=jungle-wallet
export SQS_ENDPOINT=http://localhost:${SQS_PORT:-54566}
export BROKER_CREDENTIALS_FILE="$(pwd)/work/worker.json"
go test -p=1 -race -tags=integration ./tests/integration ./internal/platform -count=1 -timeout=600s "$@"
