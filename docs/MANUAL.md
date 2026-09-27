# Roteiro de validação manual

Execute a partir da raiz do projeto. Os testes automatizados de falha usam infraestrutura descartável. Não rode `make integration` no ambiente que estiver usando manualmente: essa suíte para e reativa PostgreSQL/SQS para testar recuperação.

## Preparação

```sh
docker compose ps
BASE="http://$(docker compose port --index 1 app 8080)"
curl -fsS "$BASE/health/live"
curl -fsS "$BASE/health/ready"
```

As três réplicas devem estar healthy e os endpoints retornar live/ready. Descubra as outras portas trocando `--index 1` por 2 ou 3.

Para conferir rapidamente o fluxo completo das imagens, execute:

```sh
python3 scripts/smoke.py
```

O resultado deve ser `status: passed`, saldo `85.00`, cinco entradas no ledger e o UUID de uma carteira exclusiva do smoke. Isso não substitui a exploração manual das respostas.

## Tokens

Os clientes usam credenciais locais de exemplo. Tokens normais expiram em 120 segundos; gere outro se receber 401 após uma pausa.

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

Não publique tokens ou capturas de headers Authorization. Os exemplos de criação de carteira e envio de operações estão no README. Para cada novo teste use um playerId UUID novo, evitando o conflito esperado de jogador/moeda.

## Fluxo financeiro esperado

Crie uma carteira com 100.00 BRL. Mantenha o mesmo roundId/gameId e gere externalTransactionId/chave únicos para cada nova operação.

| Passo | Ação | Resultado esperado |
| --- | --- | --- |
| 1 | Abertura interna com 100.00 | 201, versão 1, um crédito OPENING no ledger |
| 2 | BET 25.00 | 200/PROCESSED, saldo 75.00, versão 2 |
| 3 | WIN 10.00 | 200/PROCESSED, saldo 85.00, versão 3 |
| 4 | Reenviar exatamente o BET do passo 2 | Replay=true, saldo histórico 75.00; saldo atual continua 85.00 |
| 5 | Mesma chave do BET, mudando money para 26.00 | 409; saldo/ledger inalterados |
| 6 | REFUND 25.00 referenciando o BET | Saldo 110.00, versão 4 |
| 7 | ROLLBACK 25.00 referenciando o REFUND | Saldo 85.00, versão 5 |
| 8 | LOSS 0.00 | PROCESSED, saldo 85.00, versão 5, sem novo ledger |
| 9 | Outra reversão referenciando o BET original | 422/ALREADY_REVERSED; saldo continua 85.00 |
| 10 | Ledger com limit=2, seguindo nextCursor | Cinco entradas sem repetição ou omissão |
| 11 | Reconciliação | consistent=true, storedBalance=calculatedBalance=85.00, difference=0.00 |

Use outra réplica para consultar ou reenviar o mesmo BET: a resposta e as garantias devem ser iguais.

## Negativas e pendências

- Sem token: 401. Token provider para POST /wallets: 403. Token provider-b consultando o ID da transação de provider-a: 404.
- OPENING externo, valor monetário numérico em JSON, negativo ou com mais de duas casas: 400.
- UUID inválido/nulo e limite de ledger 0/negativo/maior que 100: 400.
- REFUND antes do BET correspondente: 202/PENDING_REFERENCE. Envie o BET e consulte a transação até PROCESSED; o saldo deve retornar ao valor inicial.
- Referência já disponível na última tentativa ou após o TTL: resolve enquanto a operação ainda estiver PENDING_REFERENCE; uma rejeição terminal anterior não é reaberta.
- Referência que nunca chega: REJECTED/REFERENCE_NOT_FOUND após esgotar tentativas ou TTL. As retentativas usam backoff, não são instantâneas.
- Saldo inicial zero: versão 1, sem OPENING/ledger; primeiro WIN positivo gera versão 2 e o primeiro crédito.

Para os cenários SQS, use os envelopes e credenciais descritos em EVENTS.md. MessageGroupId é o walletId e MessageDeduplicationId é o messageId. O mesmo comando em HTTP e SQS deve ter o mesmo externalTransactionId, chave e campos financeiros. Não altere o envelope ao reutilizar um messageId.

## Evidência a observar

Verifique códigos HTTP, status/failureCode, replay, saldo atual versus histórico, versão, quantidade de lançamentos e resultado da reconciliação. `/metrics` exige token do client metrics-reader. `/health/live` e `/health/ready` são públicos. Logs JSON estão disponíveis com `docker compose logs app`; não precisam conter corpos financeiros ou tokens para permitir rastreamento.

O smoke cria dados de teste persistentes e imprime seus IDs. Nenhum comando deste roteiro apaga volumes. `docker compose down` preserva os dados; `down --volumes` não faz parte do roteiro manual.
