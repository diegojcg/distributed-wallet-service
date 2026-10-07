# Manual verification walkthrough

Run commands from the repository root. Automated failure tests use disposable infrastructure. Do not run `make integration` against an environment you are exploring manually: it stops and restarts PostgreSQL/SQS to test recovery.

For the Postman interface, use the [collections and import instructions](postman/README.md).

## Preparation

Start the stack with `docker compose up --build --scale app=3 -d --wait`, then:

```sh
docker compose ps
BASE="http://$(docker compose port --index 1 app 8080)"
curl -fsS "$BASE/health/live"
curl -fsS "$BASE/health/ready"
```

All three replicas should be healthy, with live/ready responses. Discover the other ports by replacing `--index 1` with 2 or 3.

For a quick authenticated check of the running images:

```sh
python3 scripts/smoke.py
```

Expect `status: passed`, balance `85.00`, five ledger entries, and the UUID of a unique smoke-test wallet. The script complements manual response inspection.

## Tokens

Clients use local demo credentials. Normal tokens expire after 120 seconds; request another if a pause leads to 401.

```sh
token() {
  curl -fsS http://localhost:58080/realms/jungle/protocol/openid-connect/token \
    -d grant_type=client_credentials -d "client_id=$1" \
    -d "client_secret=$1-local-secret" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])'
}
TOKEN_INTERNAL=$(token wallet-internal)
TOKEN_PROVIDER=$(token provider-a)
TOKEN_OTHER=$(token provider-b)
```

Do not publish tokens or screenshots of Authorization headers. The [README](../README.md#authentication-and-a-first-transaction) includes wallet creation and transaction examples. Use a new playerId UUID for each run to avoid the expected player/currency uniqueness conflict.

## Expected financial flow

Create a wallet with 100.00 BRL. Keep roundId/gameId consistent and use a unique externalTransactionId and idempotency key for each new operation.

| Step | Action | Expected result |
| --- | --- | --- |
| 1 | Internal opening with 100.00 | 201, version 1, one OPENING ledger credit |
| 2 | BET 25.00 | 200/PROCESSED, balance 75.00, version 2 |
| 3 | WIN 10.00 | 200/PROCESSED, balance 85.00, version 3 |
| 4 | Resend the exact BET from step 2 | Replay=true, historical balance 75.00; current balance remains 85.00 |
| 5 | Same BET key, changing money to 26.00 | 409; balance/ledger unchanged |
| 6 | REFUND 25.00 referencing the BET | Balance 110.00, version 4 |
| 7 | ROLLBACK 25.00 referencing the REFUND | Balance 85.00, version 5 |
| 8 | LOSS 0.00 | PROCESSED, balance 85.00, version 5, no new ledger entry |
| 9 | Another reversal of the original BET | 422/ALREADY_REVERSED; balance remains 85.00 |
| 10 | Ledger with limit=2, following nextCursor | Five entries without duplicates or omissions |
| 11 | Reconciliation | consistent=true, storedBalance=calculatedBalance=85.00, difference=0.00 |

Query or replay the same BET through another replica: results and guarantees should be the same.

## Negative cases and pending references

- No token: 401. Provider token for POST /wallets: 403. Provider-b token querying provider-a's transaction ID: 404.
- External OPENING, JSON numeric money, negative amounts, or more than two decimal places: 400.
- Invalid/nil UUID and a ledger limit of zero, negative, or greater than 100: 400.
- REFUND before its BET: 202/PENDING_REFERENCE. Submit the BET and query until PROCESSED; the balance should return to its initial value.
- Reference available on the last attempt or after TTL: resolves while the operation is still PENDING_REFERENCE; a previous terminal rejection is not reopened.
- Reference never arrives: REJECTED/REFERENCE_NOT_FOUND after retry/TTL exhaustion. Retries use backoff and are not immediate.
- Zero initial balance: version 1, no OPENING/ledger entry; the first positive WIN produces version 2 and the first credit.

For SQS scenarios, use the [envelopes and credentials](EVENTS.md). MessageGroupId is walletId; MessageDeduplicationId is messageId. The same command over HTTP and SQS must retain externalTransactionId, key, and financial fields. Do not change the envelope when reusing messageId.

## Evidence to inspect

Check HTTP codes, status/failureCode, replay, current versus historical balance, version, entry count, and reconciliation. `/metrics` requires the metrics-reader client token. `/health/live` and `/health/ready` are public. JSON logs are available with `docker compose logs app`; tracing does not require logging financial bodies or tokens.

The smoke test creates persistent test data and prints its IDs. This walkthrough does not delete volumes. `docker compose down` preserves data; `down --volumes` is not part of manual verification.
