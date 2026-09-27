#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p work
docker compose up -d --wait postgres keycloak broker
docker compose run --rm broker-init
umask 077
for identity in worker provider-a provider-b observer; do
  docker compose run --rm --no-deps --entrypoint cat broker-init "/credentials/$identity.json" > "work/$identity.json"
done
DATABASE_URL=postgres://jungle_admin:local-admin-only@localhost:${POSTGRES_PORT:-55432}/jungle?sslmode=disable go run ./cmd/migrate up
