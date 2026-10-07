# Guarantees and test coverage

This matrix connects the PoC's intended guarantees to implementation mechanisms and executable checks. Test names refer to Go tests or their subtests under `internal/` and `tests/integration/`. See [validation evidence](VALIDATION.md) for dated execution results; the matrix itself is not evidence of a fresh run.

| Guarantee | Implementation | Verification |
| --- | --- | --- |
| OIDC token validation | go-oidc, RS256/JWKS, issuer/audience/expiry | TestService/AuthAndProviderIsolation; TestContracts/RealExpiredTokenAndAudience |
| Provider isolation and internal operations | provider_id query filters; service_role/provider_id claims | TestService/AuthAndProviderIsolation; rejected requests without effects in TestContracts |
| Broker IAM | infra/ministack/bootstrap.py; separate provider queues | Bootstrap allow/deny; SQSAndHTTPIdempotency; PermanentFailureAndPoisonMessages |
| Fx lifecycle and shutdown | platform.Module, constructors, hooks, contexts/timeouts | TestModuleGraphWithoutInfrastructure; TestFxLifecycle; harness stop/restart and crash tests |
| Independent, encapsulated domain | Money, Wallet, WagerTransaction, WalletLedgerEntry; value snapshots | TestWalletInvariants; TestTransactionDecisions; TestOverflowOpeningLedgerAndEvents |
| Precision, scale, currency, overflow | Money int64, BIGINT; exact NUMERIC reconciliation sum | TestMoneyParsing; TestMoneyArithmeticBoundaries; TestMoneyJSON; FuzzMoneyRoundTrip; persisted overflow in TestContracts |
| Per-kind zero rules and internal OPENING | Operation.Validate, NewOpeningTransaction, internal/external schema constraints | TestOperationNormalization; ExternalOpeningZerosAndOverflow; SQS poison OPENING |
| Atomic opening, uniqueness, version | Store.Open; constraints/triggers; initial version 1 | TestOverflowOpeningLedgerAndEvents; DatabaseGuards; positive/zero opening and reconciliation |
| BET/WIN/LOSS rules | WagerTransaction.Evaluate and Wallet | TestTransactionDecisions; ReversalPolicyAndLoss; Two80BetsAcrossProcesses |
| REFUND/ROLLBACK and one successful reversal | Evaluate, one_successful_reversal, SQL context checks | TestReversalDecisions; TestReferenceContextAndKind; ReversalPolicyAndLoss; RefundAndRollbackRace |
| Consistent time across instances | clock_timestamp after lock; wallet/transaction timestamp floor | TestStoredFutureTimestamps: Apply, Consume, recovery, FAILED |
| Immutable terminal states | Domain transitions and guard_transaction | TestPendingAndFailureStates; DatabaseGuards |
| Auditable FAILED versus transient retry | store/failure.go; rollback before audit | TestPermanentClassification; TestPermanentFailureAndPoisonMessages; TestDependencyOutages |
| Canonical hash, dual uniqueness, historical balance | Operation.Hash; replay; provider-scoped constraints | TestOperationNormalization; 50DuplicatesAndHistoricalReplay; SQSAndHTTPIdempotency |
| Distributed concurrency without lost updates | Wallet SELECT FOR UPDATE, version-checked UPDATE, schema | Two80BetsAcrossProcesses including both replays; 50DuplicatesAndHistoricalReplay; HTTPAndSQSOverlapUnderWalletLock; three real processes |
| Independent wallets | No global application lock on financial path | IndependentWallets with a blocked wallet; ParallelIndependentWallets with 80 operations/eight wallets |
| Append-only ledger and consistent balance | REVOKE, triggers, unique/deferred checks; migration 003 | DatabaseGuards; TestStorageAudit; SQL failure after operation update in PermanentFailureAndPoisonMessages; reconciliation |
| Domain/inbox/outbox atomicity | Store.run in one SQL transaction; separate FAILED audit after rollback | SQSAndHTTPIdempotency; SQSCommitBeforeACKCrash; PermanentFailureAndPoisonMessages |
| Pending references, backoff, TTL, recovery | store/references.go + Evaluate; persisted metadata | PendingReferenceRecovery; ReferenceExpiryAndRejectedReference; TestAvailableReferenceWinsAtRetryBoundary; TestBlockedReferenceDoesNotStopBatch; AllProcessesRestart |
| Inbox conflicts and actual deduplication | Whole-envelope hash; consumer/messageId primary key | TestDecodeMessageContract; ten actual receives in SQSAndHTTPIdempotency; changed payload with the same messageId reaches DLQ |
| ACK after commit, redrive, visibility release | workers.handleMessage; visibility 30s, processing 10s, redrive 5 | TestSQSCommitBeforeACKCrash; poison/permanent failures; stop cycles |
| Concurrent outbox, leases, retries | SKIP LOCKED claims, 30s leases, confirmation tokens | TestConcurrentClaims; OutboxPublishedByConcurrentWorkers; PublishBeforeConfirmationCrash with real observer; TestStorageAudit/LeaseRecoveryAndStaleWorkerFencing; BrokerUnavailableKeepsCommittedEvents |
| Typed events and immutable payloads | domain/events.go; guard_outbox; EVENTS.md | TestOverflowOpeningLedgerAndEvents; CorrelationAndImmutableEventSnapshot |
| Idempotency after response loss | Result persisted before response | TestHTTPCommitCrash |
| Restart of every instance | Financial state, pending references, and events in PostgreSQL | AllProcessesRestart; TestDependencyOutages with full stop/restart |
| HTTP contract and pagination | platform/http.go; store/ledger.go; ARCHITECTURE.md | TestService; TestContracts; TestHTTPBoundaryAudit; DuplicateOpeningAndMissingKey; TestHTTPErrorContractAndDiagnosticIDs; smoke.py |
| Consistent reconciliation without repair | REPEATABLE READ READ ONLY, exact arithmetic, log/counter | Two80BetsAcrossProcesses; ParallelIndependentWallets; ReconciliationReportsDriftWithoutRepair |
| Public health and dependency checks | PostgreSQL ping and authenticated SQS query | TestFxLifecycle; TestDependencyOutages; health of three final replicas |
| Observability | JSON logs, protected metrics, persisted correlation | CorrelationAndImmutableEventSnapshot; ReconciliationReportsDriftWithoutRepair; outage metrics |
| Reversible migrations and clean setup | Migrations 001–003; scripts/clean-check.sh | TestMigrationRollback up/down/up; make clean-check |
| Formatting, race detection, analysis | README/Makefile commands | gofmt; go test; go test -race; go vet; govulncheck; VALIDATION.md |

## Policy boundaries

A reference permits one successful reversal across both reversal kinds. Rolling back a refund is allowed without making the original bet eligible for another reversal. Synchronous processing has no intermediate PENDING commit; PENDING_REFERENCE is durable for recovery. FAILED is distinct from a business rejection and emits no WagerTransactionRejected event.

Parallel tests establish correctness for the exercised scenarios, not capacity. Double-entry accounting, dashboards, OpenTelemetry, and load-test percentiles are outside the current implementation. MiniStack durability is not claimed equivalent to AWS SQS, and the environment is not production-ready. See [architecture](../ARCHITECTURE.md) for operational tradeoffs and [origin](ORIGIN.md) for the initial specification.
