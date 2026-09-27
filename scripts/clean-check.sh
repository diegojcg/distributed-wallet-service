#!/bin/sh
# Rebuild the deliverable in a new directory and disposable Compose project.
set -eu
cd "$(dirname "$0")/.."
source_dir=$(pwd)
mkdir -p work
verify_dir=$(mktemp -d "$source_dir/work/clean-check.XXXXXX")
export COMPOSE_PROJECT_NAME="jungle-verify-$(date +%s)-$$"
export POSTGRES_PORT=${VERIFY_POSTGRES_PORT:-55433}
export SQS_PORT=${VERIFY_SQS_PORT:-54567}
export KEYCLOAK_PORT=${VERIFY_KEYCLOAK_PORT:-58081}
tar --exclude=.git --exclude=work --exclude=.env --exclude=bin --exclude=coverage.out -cf - . | tar -xf - -C "$verify_dir"
cd "$verify_dir"
cleanup() { docker compose down --volumes --remove-orphans; }
trap cleanup EXIT
printf 'Verification directory: %s\n' "$verify_dir"
go test -race ./...
go vet ./...
go mod verify
./scripts/infra-up.sh
./scripts/integration.sh -v
docker compose up --build --scale app=3 -d --wait
docker compose ps
for instance in 1 2 3; do
 address=$(docker compose port --index "$instance" app 8080)
 curl --fail --silent --show-error "http://$address/health/ready"
done
printf '\nClean environment checks passed.\n'
