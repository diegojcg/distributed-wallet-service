.PHONY: infra up test race vet integration down migrate-up migrate-down clean-check
infra:
	./scripts/infra-up.sh
up:
	docker compose up --build --scale app=3 -d --wait
# Down retains named volumes. Resetting data is deliberately a separate manual step.
down:
	docker compose down
test:
	go test ./...
race:
	go test -race ./...
vet:
	go vet ./...
integration:
	./scripts/integration.sh
migrate-up:
	go run ./cmd/migrate up
migrate-down:
	go run ./cmd/migrate down

clean-check:
	./scripts/clean-check.sh
