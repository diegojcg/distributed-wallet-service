# Messaging contracts — version 1

## Input and authorization

`wager-transactions.fifo` accepts provider-a commands; `provider-b-wager-transactions.fifo` accepts provider-b commands. Bootstrap binds each queue to its IAM-authorized identity. The application compares providerId against the queue's configured provider independently of the received body.

```json
{
  "messageId": "msg-bet-123",
  "correlationId": "request-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-27T12:00:00Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "bet-123",
    "idempotencyKey": "provider-a:bet-123",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-123",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": {"amount": "25.00", "currency": "BRL"}
  }
}
```

Use `MessageGroupId=walletId` and `MessageDeduplicationId=messageId`. The messageId must be unique across providers for this consumer. Optional correlationId defaults to messageId (or the envelope hash if the ID exceeds 128 characters). Event causationId is the original messageId. Explicit correlation IDs allow up to 128 visible ASCII characters; messageId allows 200. The inbox hash covers the complete envelope bytes, including metadata. Resend the exact same envelope when reusing a message identity.

Bodies must contain only known fields. External OPENING, provider spoofing, and invalid messages receive no ACK and reach the DLQ after five receives. Business rejections and durable pending references receive ACK after commit. FAILED is audited and redriven to the DLQ; transient unavailability rolls back the attempt, leaving the command available for retry.

## Output and routing

All events go to `wallet-events.fifo`. Only the observer identity can consume them. No broker network call happens inside the financial transaction. `MessageGroupId=aggregateId` (walletId); `MessageDeduplicationId=eventId`. Publication retries preserve eventId.

```json
{
  "eventId": "0192f298-345e-7e38-af88-e43f851a819d",
  "eventType": "WalletBalanceChanged",
  "aggregateId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "correlationId": "request-123",
  "causationId": "msg-bet-123",
  "occurredAt": "2026-09-27T12:00:01Z",
  "version": 1,
  "data": {
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "transactionId": "0192f298-345e-7e38-af88-e43f851a8100",
    "direction": "DEBIT",
    "money": {"amount": "25.00", "currency": "BRL"},
    "balanceBefore": {"amount": "100.00", "currency": "BRL"},
    "balanceAfter": {"amount": "75.00", "currency": "BRL"},
    "walletVersion": 2
  }
}
```

| eventType | data fields | When emitted |
| --- | --- | --- |
| WagerTransactionProcessed | transactionId, walletId, playerId, providerId, externalTransactionId, kind, money, balance | Processed operation, including LOSS and OPENING; external metadata is absent for opening |
| WagerTransactionRejected | transactionId, walletId, providerId, externalTransactionId, kind, failureCode | Terminal business rejection |
| WagerTransactionPendingReference | transactionId, walletId, providerId, externalTransactionId, referenceExternalTransactionId, nextAttemptAt | Initial wait state; retries do not repeat this event |
| WalletBalanceChanged | walletId, transactionId, direction, money, balanceBefore, balanceAfter, walletVersion | Actual financial change |

Domain constructors define type and version. Encoded payloads are private, returned by copy, and protected from database mutation. Dates use UTC/RFC3339; money uses strings with two decimal places. For HTTP, correlationId comes from X-Correlation-ID or a generated UUID returned in that header; causationId is absent. Pending-reference recovery preserves the original metadata.

Output consumers must validate eventType/version, persist deduplication by eventId, and commit their own update before deleting the message. Delivery is at-least-once. Concurrent publishers can publish wallet versions out of financial order; projections must detect walletVersion gaps and reconcile. Do not recalculate balances from delivery order without that protection. Local retention is provided by the emulator; this environment does not establish real SQS durability.
