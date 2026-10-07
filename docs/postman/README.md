# Postman verification

Import the [local environment](Distributed-Wallet-local.postman_environment.json) and either or both collections:

- [Manual walkthrough](Distributed-Wallet.postman_collection.json): 28 ordered requests covering opening, all five operation types, replay, pagination, reconciliation, and contract/authorization errors.
- [HTTP concurrency](Distributed-Wallet-concurrency.postman_collection.json): five independent scenarios with parallel requests and financial-state verification.

## Setup

From the repository root, start the application and discover the ports:

```sh
docker compose up --build --scale app=3 -d --wait
docker compose port --index 1 app 8080
docker compose port --index 2 app 8080
docker compose port --index 3 app 8080
```

Select **Distributed Wallet local** in Postman and set `baseUrl` to the first URL, including `http://`. The initial value `http://127.0.0.1:58000` is for direct Go execution; Compose ports are dynamic and must be checked after recreating containers. `keycloakUrl` defaults to `http://localhost:58080`.

Protected requests automatically obtain fresh tokens using client_credentials. The environment contains local demo client secrets, without exported tokens.

## Manual walkthrough

Run items 00–27 in order with **Send** or **Run collection**. Request 02 creates a new wallet, generates unique player/operation IDs, and stores them in collection variables. Do not override these with stale environment variables.

Expected final state: balance **85.00**, version **5**, **five entries**, and consistent reconciliation. BET replay returns historical balance **75.00** without changing the current balance. Negative tests expect 400, 401, 403, 404, 409, and 422. Restart by running item 02 again, then continue in order.

## Concurrency

Set `replicaUrls` in the concurrency collection to the three instance URLs, separated by commas. If the environment defines the same variable, that value takes precedence. An empty value falls back to `baseUrl` only.

Use **Run collection**, select all five items, set **Delay = 0**, and start with one iteration, then repeat with three or five. Each scenario can also run independently using **Send** and creates its own data. Wallet IDs appear in the Console.

| Scenario | Expected result |
| --- | --- |
| Two BETs of 80 against a balance of 100 | One accepted, one rejected; balance 20; replays preserve both results |
| 50 copies of the same BET of 25 | One debit, 49 replays; balance 75; replay stays historical after WIN |
| Same key with BET 25 and BET 30 | One accepted, one 409; winner determines balance, only one debit |
| REFUND and ROLLBACK of the same BET | One reversal accepted, one ALREADY_REVERSED |
| 80 BETs across eight wallets | Ten debits of 1 per wallet; each ends at balance 90 and version 11 |

Each item's health GET starts the scenario. Its post-response script performs setup, asynchronous requests with pm.sendRequest/Promise.all, and verification. Inspect **Test Results**, not just the GET's HTTP 200. The Runner visits items sequentially; concurrency happens inside each item. Iterations repeat scenarios without increasing parallel group sizes.

The collection creates 12 wallets per iteration and preserves their data. It checks balance/version through every URL, ledger uniqueness/sum, and reconciliation. These are correctness checks under competing requests, without a latency target or benchmark claim. HTTP clients cannot guarantee simultaneous arrival at the database; the Go suite separately proves HTTP/SQS overlap through observed PostgreSQL lock waits.

## Failures and recovery

The Go suite runs crash, SQS, and dependency-outage scenarios in disposable infrastructure:

```sh
./scripts/clean-check.sh -run '^(TestHTTPCommitCrash|TestSQSCommitBeforeACKCrash|TestPublishBeforeConfirmationCrash|TestDependencyOutages|TestPermanentFailureAndPoisonMessages)$'
```

Run `make clean-check` for the full suite. It uses another Compose project, empty volumes, and separate ports. Do not use `make integration` against your manual-test environment: that variant interrupts dependencies in the current project.

## Evidence

The September 27–28, 2026 runs used Newman 6.2.2 with the application, PostgreSQL, Keycloak, and broker in a separate environment:

- Manual: 28 requests, 25 auxiliary authentication requests, 76 assertions, zero failures.
- Concurrency: three iterations across three replicas, 705 requests including setup/queries, 480 assertions, zero failures. With only one URL, the same three iterations check 324 assertions.

The English collections were rerun on October 7, 2026 in fresh infrastructure with three replicas: the same 76 manual and 480 concurrency assertions passed, with zero failures.

See [validation evidence](../VALIDATION.md) for dated runs and scope. Official documentation: [Collection Runner](https://learning.postman.com/docs/tests-and-scripts/running-collections/intro-to-collection-runs) and [Newman](https://learning.postman.com/docs/reference/newman-cli/installing-running-newman/).
