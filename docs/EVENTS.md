# Contratos de mensageria — versão 1

## Entrada e autorização

`wager-transactions.fifo` aceita comandos de provider-a; `provider-b-wager-transactions.fifo`, de provider-b. O bootstrap vincula cada fila à identidade autorizada por IAM. A aplicação compara o providerId com o provedor configurado da fila, independentemente do corpo recebido.

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

Use MessageGroupId=walletId e MessageDeduplicationId=messageId. O messageId deve ser único entre os provedores para este consumidor. O campo correlationId é opcional; quando ausente, usa-se messageId (ou o hash do envelope se o ID exceder 128 caracteres). CausationId dos eventos é o messageId original. A correlação explícita tem até 128 caracteres ASCII visíveis; messageId tem até 200. O hash da inbox cobre os bytes completos do envelope, inclusive os metadados. Reenvie exatamente o mesmo envelope para reapresentar a mesma identidade de mensagem.

O corpo deve conter apenas campos conhecidos. OPENING externo, falsificação de provedor e mensagens inválidas não recebem ACK e chegam à DLQ após cinco recebimentos. Rejeição de negócio e pendência durável recebem ACK após commit. FAILED é auditado e segue para DLQ; indisponibilidade transitória desfaz a tentativa, mantendo o comando disponível para retry.

## Saída e roteamento

Todos os eventos vão para `wallet-events.fifo`. Somente a identidade observer pode consumi-los. Não há chamada de rede ao broker dentro da transação financeira. MessageGroupId=aggregateId (walletId); MessageDeduplicationId=eventId. O eventId permanece idêntico em tentativas de publicação.

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

| eventType | Campos de data | Ocorrência |
| --- | --- | --- |
| WagerTransactionProcessed | transactionId, walletId, playerId, providerId, externalTransactionId, kind, money, balance | Operação processada, incluindo LOSS e OPENING; metadados externos ausentes na abertura |
| WagerTransactionRejected | transactionId, walletId, providerId, externalTransactionId, kind, failureCode | Rejeição definitiva |
| WagerTransactionPendingReference | transactionId, walletId, providerId, externalTransactionId, referenceExternalTransactionId, nextAttemptAt | Primeiro registro de espera; retries não repetem esse evento |
| WalletBalanceChanged | walletId, transactionId, direction, money, balanceBefore, balanceAfter, walletVersion | Alteração financeira efetiva |

Tipo e versão são definidos pelos construtores do domínio. O payload codificado é privado, devolvido por cópia e protegido contra alteração no banco. Datas são UTC/RFC3339; valores monetários são strings com duas casas. Em HTTP, correlationId vem de X-Correlation-ID, ou de UUID gerado e devolvido no mesmo header. CausationId fica ausente. A retomada de pendências preserva os metadados originais.

O consumidor de saída deve validar eventType/version, deduplicar persistentemente por eventId e confirmar sua própria atualização antes de remover a mensagem. A semântica é at-least-once. Publishers concorrentes podem publicar versões da mesma carteira fora da ordem financeira; uma projeção deve identificar lacunas por walletVersion e reconciliar. Não use a ordem de entrega para recalcular saldo sem essa proteção. A retenção local é a do emulador; esse ambiente não substitui a durabilidade do SQS real.
