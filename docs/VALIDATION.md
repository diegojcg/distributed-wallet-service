# Validation evidence

## Baseline: September 27–28, 2026

Completed runs on Linux amd64, Go 1.27.1, Docker Engine 25.0.2, and Compose 2.19.1. Real dependencies: PostgreSQL 17.6, Keycloak 26.7.4, and MiniStack 1.5.17 with AUTH=true.

| Check | Result |
| --- | --- |
| `go test ./...` and `go test -race ./...` | Passed |
| `go vet ./...` and `go vet -tags=integration ./...` | Passed |
| `go mod verify` | All modules verified |
| `gofmt -l cmd internal migrations tests` | No unformatted files |
| `GOTOOLCHAIN=go1.27.1 go run golang.org/x/vuln/cmd/govulncheck@latest ./...` | No known vulnerabilities found in the September 27 run |
| Final full `scripts/clean-check.sh` run | Passed: integration with race detector in 308.358s; platform package with Fx lifecycle in 1.041s |
| Image build and three replicas in clean infrastructure | Healthy; authenticated smoke passed |
| Three-replica smoke in the main environment | Passed: balance 85.00, version 5, five entries, consistent reconciliation |
| IAM file permissions inside replicas | Worker readable; provider-a/provider-b/observer inaccessible to application UID |
| `FuzzMoneyRoundTrip`, 20 seconds | 10,229,670 executions without failure; not a service benchmark |
| `docker compose config --quiet` and `sh -n scripts/*.sh` | Passed |
| Manual Postman collection, Newman 6.2.2 | 28 requests, 25 auxiliary authentication requests, 76 assertions, zero failures |
| Concurrent Postman collection, Newman 6.2.2 | Three iterations across three replicas: 705 requests, 480 assertions, zero failures |

These are dated completed runs, not checks performed automatically whenever this repository is viewed. Importable collections are in [postman/README.md](postman/README.md). The initial specification remains linked from [origin and attribution](ORIGIN.md).

## English PoC presentation: October 7, 2026

This revision changes documentation and Postman display text/metadata. Application code, migrations, runtime configuration, request contracts, and assertion logic are unchanged.

Fresh checks used a disposable Compose project with empty volumes, newly built application images, three healthy replicas, PostgreSQL, Keycloak, and the authenticated broker:

| Check | Result |
| --- | --- |
| English manual collection, Newman 6.2.2 | 53 requests including authentication; 76 assertions; zero failures |
| English concurrency collection, Newman 6.2.2 | Three iterations across three replicas; 705 requests; 480 assertions; zero failures |
| Authenticated smoke | Passed across three replicas: balance 85.00, five ledger entries, historical replay, authorization, and reconciliation |
| README transaction example | Passed with the isolated environment's ports: BET processed, balance 75.00, two ledger entries, consistent reconciliation, provider lookup |
| Postman structural comparison | All 59 scripts parse; only mapped display strings and metadata differ from the previous collections |
| Documentation and diff checks | Local Markdown links resolve; JSON parses; no whitespace errors |

The full Go integration/crash suite was not rerun for this documentation-only revision; its baseline evidence is recorded above. The temporary containers and volumes were removed after verification.

## Reproduce in an isolated environment

```sh
make clean-check
```

The script copies sources, creates a separate Compose project and empty volumes, provisions PostgreSQL, queues, IAM policies/users, Keycloak realm/clients, and migrations. It runs unit/race checks, vet, module verification, and integration; then builds the final image, starts three replicas, and runs the smoke test. Cleanup removes only this disposable environment's containers and volumes.

Default ports are 55433, 54567, and 58081, overridable with `VERIFY_POSTGRES_PORT`, `VERIFY_SQS_PORT`, and `VERIFY_KEYCLOAK_PORT`. The copied source directory remains in `work/` for inspection. Logs and generated credentials stay outside Git.

The suite uses three processes with separate pools and memory, compiled with `-race`. Failpoints exist only in test builds; marker files prove each interruption occurred. Direct storage/migration tests use temporary databases and separate administrator/runtime connections.

## Verified scenarios

| Guarantee | Evidence |
| --- | --- |
| OIDC and isolation | Real tokens: missing, invalid, expired, wrong audience, roles, and provider isolation; denied access without financial effects |
| Exact money | Parsing, precision, overflow, zero by operation kind, internal OPENING, external OPENING rejected over HTTP/SQS |
| Cross-instance concurrency | Two bets of 80 against 100, both replayed; 50 duplicates; eight wallets with 80 concurrent operations |
| Overlapping HTTP/SQS | HTTPAndSQSOverlapUnderWalletLock requires two connections in the same lock wait chain before release; one debit/operation and no duplicate inbox/outbox effects |
| Reversals | Simultaneous REFUND/ROLLBACK of one BET: one processed, one ALREADY_REVERSED; LOSS preserves balance/version |
| Pending references | Recovery, expiry, unsuccessful reference, available reference at the last retry/after TTL, and continuation after a blocked wallet |
| Shared clock | TestStoredFutureTimestamps injects distinct future wallet/transaction timestamps and checks Apply, Consume, recovery, and FAILED |
| Privileges and immutability | Runtime receives 42501 when editing/deleting/truncating ledger or disabling guards; triggers reject administrator mutations with 23514 |
| SQL integrity | Negative balance, balance without ledger, invalid identity/version, changed outbox payload, and ledger linked to the wrong wallet rejected |
| Atomicity | Serialization failure during outbox INSERT rolls back inbox, operation, balance, and ledger; permanent SQL failure rolls back financial effects before FAILED audit |
| Idempotency | Dual uniqueness, cross-key conflicts, normalized hash, historical balance, restart persistence, conflicting inbox without another debit |
| Concurrent outbox | Simultaneous claims receive disjoint batches; active leases prevent reclaim |
| Lease recovery and stale worker fencing | Expired lease gets a new token with the same ID/payload; old-token confirmation/rescheduling cannot change the new lease |
| Publication after crash | Observer consumes the real queue and compares payload, eventId, MessageDeduplicationId, and MessageGroupId against recovered outbox data |
| Outages and restart | Full process restart; PostgreSQL outage returns transient errors; broker outage preserves committed events for later publication |
| Actual crashes | Termination after HTTP commit, SQS commit/before ACK, and publication/before outbox confirmation; recovery by another instance |
| HTTP and pagination | Equivalent/nil UUIDs, required fields, duplicate opening, missing key, full ledger traversal, malformed/cross-wallet cursors |
| UUID lookup | EXPLAIN of the typed query confirmed primary-key Index Scan with provider filtering; the optimizer may choose sequential scans for small tables |
| Events and observability | Persisted correlation/causation, immutable snapshots, no new replay events, available IDs in error logs |
| Reconciliation | REPEATABLE READ, detection of test-injected corruption, divergence response/log/metric, no silent repair |
| Fx and migrations | Graph validation without infrastructure, real start/stop, listener closure, migrations over existing data, temporary-database up/down/up |

The [coverage matrix](REQUIREMENTS.md) maps guarantees to executable tests. The [manual walkthrough](MANUAL.md) describes expected responses.

## Method and limits

The HTTP/SQS overlap test holds the wallet lock, waits for the consumer to reach the database, starts HTTP, and follows the blocking chain to require both inputs to be waiting. This separates broker delivery time from the processing deadline.

Inbox tests allow a 30-second visibility window after a lost long-poll response during previous shutdown. DLQ waits allow up to 90 seconds to cover that window and backoff across five deliveries while still requiring the exact message and no additional debit. The suite timeout is ten minutes.

In the publish-after-crash test, FIFO deduplication may suppress the second send within the broker's window. Evidence combines a new outbox attempt with the identity/content of the received event; it does not claim two observed deliveries within that window.

Results cover the described scenarios, not capacity or production certification. Emulator crash durability and real AWS deployment were not tested. Publication does not guarantee strict wallet order; consumers must handle duplicates and walletVersion gaps. See [architecture](../ARCHITECTURE.md) for the decisions and limitations.
