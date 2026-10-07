# Architecture and decisions

## Scope

A wallet service built with Go 1.27.1, PostgreSQL 17, pgx with explicit SQL, Uber Fx, Keycloak, and SQS through MiniStack. It demonstrates consistency and recovery under competing requests and interrupted processes. No real AWS services are called. See [origin](docs/ORIGIN.md), [coverage](docs/REQUIREMENTS.md), and [validation evidence](docs/VALIDATION.md).

## Money and domain

`Money` stores `int64` cents and a currency, without floating point. Its internal range is −92,233,720,368,547,758.08 through 92,233,720,368,547,758.07. External parsing rejects signs, whitespace, scientific notation, nonfinite values, more than two fractional digits, and overflow. `25`, `025.0`, and `25.00` normalize to `25.00`. The value object recognizes BRL, USD, and EUR; the application operates in BRL only. Its Go zero value is invalid. Internal arithmetic can be negative; wallet balances cannot.

`Wallet`, `WagerTransaction`, and `WalletLedgerEntry` have private state, value snapshots, and separate creation/rehydration paths. `Evaluate` owns financial rules and transitions without I/O; the Store coordinates persistence and locks. A wallet starts at version 1, with validated debit/credit methods. Only balance changes increment its version. A positive opening creates an internal OPENING transaction and ledger entry at version 1; a zero opening creates neither. LOSS leaves balance and version unchanged. UUIDs are normalized in the hash; external identifiers are case-sensitive, are not silently trimmed, and cannot have leading/trailing spaces.

## SQL transactions and concurrency

The Store is the transactional use case and unit of work. Keeping SQL coordination in one package avoids repository interfaces with a single implementation; the tradeoff is that coordination tests require real PostgreSQL. Financial decisions remain pure domain logic. No nested repository opens a separate transaction. Each operation commits the business transaction, balance, ledger, outbox, and SQS inbox together. Operations without dependencies do not commit an intermediate PENDING state.

READ COMMITTED with `SELECT FOR UPDATE` on the wallet serializes changes to that wallet. After acquiring the lock, the UPDATE also checks the previously read version. Lock ordering is wallet first, then operation mutation. Different wallets share no application lock. Deadlocks, outages, and timeouts roll back the attempt and return retry/503. A network error during COMMIT can have an ambiguous outcome, resolved by an idempotent retry.

Persisted financial timestamps use PostgreSQL `clock_timestamp()` after acquiring the lock. A transition uses the maximum of that clock and the wallet/transaction timestamps already stored. This preserves monotonicity even for older data written by a process with a fast clock. Opening and FAILED audit records use the same time source. Retry eligibility uses database time; local durations use the process's monotonic clock. Transaction lookup uses the typed predicate `id=$2`, without casting the column to text, so the primary-key index remains usable.

The database enforces nonnegative balances, foreign keys, transaction/ledger uniqueness, immutable ledger entries, and immutable terminal states. Deferred triggers check transaction–ledger correspondence, financial direction, ledger continuity, and balance equality with the sum of entries. The runtime identity `jungle_app` does not own tables and lacks UPDATE/DELETE/TRUNCATE on the ledger; migrations use a separate identity.

**Known cost:** deferred consistency checks scan the wallet's ledger. This provides a testable defense for the PoC, but its cost grows with history. A production design would need measurement and an equally protected incremental strategy. No throughput target is claimed.

## Idempotency

Unique constraints cover `(provider_id,idempotency_key)` and `(provider_id,external_id)`. `INSERT ON CONFLICT DO NOTHING` is followed by a separate SELECT under READ COMMITTED when requests compete. Keys pointing to different records produce 409. A new key for the same external operation and payload returns the original result; that alternative key is not persisted as an alias.

The business hash is SHA-256 over JSON emitted from maps with sorted keys. Fields: `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount`, `money.currency`, and `referenceExternalTransactionId`. Values are strings and objects; monetary values are never JSON numbers. An absent reference becomes an empty string, UUIDs use canonical representation, and money uses two decimal places. This is deterministic contract-specific canonicalization, not a general RFC 8785 implementation.

The business hash excludes the idempotency key, `messageId`, `occurredAt`, and HTTP/SQS metadata. Terminal replay returns the balance persisted during the original execution. Rejections are durable too. Separately, the inbox hashes the complete envelope bytes: changing any byte while reusing a `messageId` is a message conflict with no additional financial effect.

## States and references

PENDING transitions to PROCESSED, REJECTED, PENDING_REFERENCE, or FAILED. PENDING_REFERENCE transitions to itself or a terminal state. Terminal states are immutable. Domain constructors and the schema reject invalid transitions.

An absent or pending reference creates a durable PENDING_REFERENCE state and one event upon entering that state. A worker resumes processing through the same wallet lock, rechecking state and deadline inside the transaction. Backoff is 1, 2, 4, 8, 16, then 32 seconds. The worker checks the reference first: an available processed reference can resolve the operation even on the tenth retry or after five minutes. Only if the reference is still absent/pending do ten retries or five minutes end the wait with REJECTED/REFERENCE_NOT_FOUND. A terminal operation is not reopened when its reference arrives later. A REJECTED/FAILED reference produces REFERENCE_UNSUCCESSFUL. Provider, player, wallet, currency, and round must match. A referenced WIN requires a BET in the same round; its amount may differ from the bet.

Each reference item has a two-second timeout within a ten-second iteration budget. Errors are logged with available IDs, and processing continues to the next item unless the iteration context is canceled. This prevents one blocked wallet from holding up the whole batch; it does not guarantee progress for every item when several consume the overall budget.

A missing wallet or mismatched player fails before the financial decision and does not create a REJECTED transaction. A missing wallet violates its foreign key; a mismatched player violates wallet ownership/context. HTTP returns 404/NOT_FOUND or 422/WALLET_OWNER_MISMATCH. SQS rolls back without committing the inbox or acknowledging the message, eventually redriving to the DLQ if the condition persists.

Transient connection errors, timeouts, cancellation, deadlocks, and serialization failures roll back and return 503/retry. Ambiguous commit attempts never become FAILED. An explicit set of PostgreSQL responses that abort an attempt is classified as permanent: 0A000 (unsupported operation), 22003 (persistence overflow), 23514 (SQL invariant violation), and 42883 (missing function). These indicate infrastructure/schema defects; expected financial rejections are handled by the domain before reaching them.

After rollback, a new transaction uses the same lock/idempotency rules to record FAILED/INFRASTRUCTURE_PERMANENT and the observed balance. For SQS, the inbox commits with this audit record. If the audit write also fails, the result remains 503/retry; no audit is invented. FAILED has no ledger entry, financial effect, or WagerTransactionRejected event. Its replay is terminal; SQS does not ACK it and redrives it to the DLQ. Outbox publication failures never reclassify an already committed financial transaction.

## Reversals

Each referenced transaction permits at most one successful REFUND or ROLLBACK, enforced by a partial unique index on `resolved_reference_id` for processed reversals. REFUND accepts only BET. ROLLBACK accepts BET, WIN, or REFUND; both reversals require the full referenced amount and matching context. Rolling back a REFUND debits the wallet and does not make the original BET eligible for another reversal. A ROLLBACK cannot itself be rolled back.

Insufficient balance for BET produces INSUFFICIENT_FUNDS; a debit reversal produces REVERSAL_INSUFFICIENT_FUNDS. Other domain codes include ALREADY_REVERSED, REFERENCE_MISMATCH, REFERENCE_KIND_INVALID, REFERENCE_AMOUNT_MISMATCH, REFERENCE_UNSUCCESSFUL, REFERENCE_NOT_FOUND, and BALANCE_OVERFLOW.

## Authentication and authorization

Keycloak provisions service clients using `client_credentials`. The API validates RS256 signatures through JWKS, issuer, audience, and expiry with go-oidc. It issues no custom tokens. `service_role` and `provider_id` are fixed IdP mappers.

| Operation | Required identity |
| --- | --- |
| Create/read wallets, ledger, reconciliation | `service_role=internal` |
| Submit/query external transactions | `service_role=provider` and `provider_id` |
| GET /metrics | `service_role=metrics` |
| GET /health/live and /health/ready | Public |

The body/path provider must match the token. ID queries filter by provider and return 404 for another provider's data. The internal service has no implicit permission to query external transactions. The public issuer URL stays fixed; Compose uses the internal network for JWKS without disabling issuer/audience validation.

## Broker and provider isolation

MiniStack 1.5.17 runs with `AUTH=true`. Bootstrap creates IAM users/policies and executes real allow/deny checks before the application starts. The worker consumes inputs and publishes events; it cannot publish commands. Each producer can send only to its own queue and cannot consume. In the mounted volume, the app process (UID 10001) can read only `worker.json` (owner 10001, mode 0400); producer/observer files belong to root with mode 0600. Local copies in `work/` use mode 0600.

Inspection showed that MiniStack's SenderId returns the account rather than the IAM UserId. Provider identity therefore comes from the provisioned queue-to-provider binding, rather than this attribute or an untrusted body field.

- provider-a: `wager-transactions.fifo` → `wager-transactions-dlq.fifo`.
- provider-b: `provider-b-wager-transactions.fifo` → `provider-b-wager-transactions-dlq.fifo`.
- Output: `wallet-events.fifo`, readable only by the observer identity.

Normal sends use `MessageGroupId=walletId` and `MessageDeduplicationId=messageId`. The consumer verifies the provider against the queue binding. Tests use distinct transport deduplication IDs to force actual deliveries of the same operation, preventing FIFO deduplication from hiding application bugs.

Visibility timeout is 30 seconds, processing timeout 10 seconds, long polling 10 seconds, and maxReceiveCount 5. Message retries use visibility backoff of 2, 4, then 8 seconds, capped at 8. Invalid/conflicting messages are not acknowledged and reach the DLQ through broker redrive. SIGTERM cancels work; unacknowledged messages attempt visibility release with an independent cleanup timeout. If release fails, visibility expires naturally. Messages are removed only after inbox/domain commit.

## Outbox

An atomic `UPDATE ... FROM SELECT FOR UPDATE SKIP LOCKED` claims up to ten events with a 30-second lease and UUID token. Publication happens outside the financial transaction. Confirmation requires the current token, fencing off an old worker. Failures use exponential backoff capped at 32 seconds, scheduled by database time; another instance can reclaim expired leases. There is no retry limit or automatic event discard. Operations should alert on `wallet_outbox_oldest_seconds` and `wallet_outbox_pending`; the PoC exposes these metrics but does not provision an external alerting system.

Republishing preserves `eventId`. Envelopes contain eventId, eventType, aggregateId, correlationId, UTC occurredAt, version, and typed data. Correlation comes from HTTP X-Correlation-ID or SQS correlationId/messageId; causationId identifies the SQS message. Metadata is persisted and preserved on reference recovery. See [message contracts](docs/EVENTS.md). The schema protects immutable payloads. All event types share the output queue; consumers route by eventType and persist deduplication by eventId.

Delivery is at-least-once, without global ordering. Concurrent publishers may publish one wallet's events out of financial order. Projections must use walletVersion and reconcile gaps. The ledger remains the financial source of truth.

## HTTP and reconciliation

| Situation | HTTP |
| --- | --- |
| Wallet created | 201 |
| Processed/successful replay/query | 200 |
| Pending reference | 202 |
| Invalid input | 400 |
| Missing/invalid/expired token | 401 |
| Identity without permission | 403 |
| Missing or isolated resource | 404 |
| Idempotency conflict/duplicate wallet | 409 |
| Business rejection | 422 |
| Audited permanent failure (FAILED) | 500, persisted result and failureCode |
| Transient unavailability | 503 + Retry-After: 1 |

Financial rejections return transactionId, status, failureCode, observed balance, and idempotentReplay. Transport errors return `{ "code": "..." }`. Reconciliation uses REPEATABLE READ READ ONLY and includes opening entries: difference = stored balance − ledger sum. It never repairs data. Divergence is logged and counted.

Accepted UUID route representations are canonicalized before lookup; nil UUIDs and invalid input return 400. Missing required business fields return 400 before identity comparison, without persistence.

Ledger pagination orders by ascending version. The base64url cursor binds the wallet and last version; the default limit is 50 and maximum 100. Clients must treat cursors as opaque.

## Lifecycle and fault injection

Fx injects configuration, pool, broker, store, authentication, metrics, workers, and HTTP. Hooks validate PostgreSQL, JWKS, and queues before serving. Shutdown cancels polling/worker work, drains HTTP, waits for workers, then closes the pool and idle HTTP client connections. Unexpected server failure triggers Fx shutdown. No worker uses a closed pool.

Normal builds contain no failpoints. `-tags=failpoints` enables abrupt termination at `after_http_commit`, `after_commit_before_ack`, and `after_publish`. A per-run marker file allows one crash followed by recovery through another instance.

## Observability

JSON logs include correlation, available IDs, status, and failure code. HTTP 503 logs include correlationId, walletId, providerId, and transactionId when known; reference retry errors include the persisted transaction identity. Logs exclude tokens, secrets, and complete financial bodies. `/metrics` requires the metrics identity and exposes:

| Metric | Purpose |
| --- | --- |
| wallet_transactions_total{status,transport} | HTTP/SQS responses and worker transitions, including input replays |
| wallet_idempotent_replays_total | HTTP/SQS replays |
| wallet_retries_total{component} | Worker retries/errors and metric refresh failures |
| wallet_concurrency_conflicts_total{reason} | Idempotency conflicts, deadlocks, serialization failures |
| wallet_dlq_messages{provider} | Approximate visible/in-flight DLQ messages |
| wallet_outbox_oldest_seconds / wallet_outbox_pending | Pending-event age and count |
| wallet_pending_references | Persisted pending references |
| wallet_processing_seconds | Use-case duration histogram |
| wallet_reconciliation_divergences_total | Divergences detected by reconciliation |

Labels use bounded sets; IDs are not labels. Queue/pending gauges represent shared state and should be aggregated with max across replicas, not sum. On refresh failure the last known gauge remains and `wallet_retries_total{component="metrics"}` increases, avoiding a false zero. Counters are process-local. Dashboards, tracing, and capacity benchmarks are outside the current scope.

## Operational limits

The local environment uses HTTP and demo credentials. Keycloak runs in start-dev mode, the broker is emulated, audit tables have no retention policy, and history validation grows with the ledger. The operational currency is BRL and the reversal policy forbids a second reversal of a reference, even of another kind. These are explicit PoC boundaries, not a production deployment template.

## Technical references

- [PostgreSQL transaction isolation](https://www.postgresql.org/docs/current/transaction-iso.html)
- [Keycloak containers](https://www.keycloak.org/server/containers)
- [MiniStack 1.5.17](https://github.com/ministackorg/ministack/tree/v1.5.17)
- [SQS message deduplication](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/using-messagededuplicationid-property.html)
