# Correções da revisão independente — 27/09/2026

A revisão motivou três correções de código e provas adicionais. A arquitetura, os contratos financeiros, o broker e a política de shutdown foram preservados.

## Correções

1. **Relógios das instâncias.** Abertura, processamento HTTP/SQS, retomada e auditoria FAILED usam o relógio do PostgreSQL. Depois do lock, timestamps respeitam também os valores já persistidos da carteira e da transação. O teste injeta timestamps futuros diferentes nos dois registros e verifica processamento, retomada e FAILED. O reagendamento da outbox também usa o relógio do banco.
2. **Referência no limite da espera.** Uma operação ainda pendente avalia a referência disponível antes de aplicar o limite de tentativas/TTL. `REFERENCE_NOT_FOUND` só ocorre quando a referência continua ausente ou pendente. Estados terminais permanecem imutáveis. Há casos unitários separados para a décima retomada e para TTL vencido.
3. **Consulta por ID.** O predicado passou de `id::text=$2` para `id=$2`; a validação/normalização de UUID permanece no handler. Isso permite usar a PK, preservando o filtro de provedor. O EXPLAIN executado no banco local confirmou `Index Scan using wager_transactions_pkey`, com filtro de provedor preservado. O otimizador ainda pode escolher varredura sequencial para tabelas pequenas.

O worker de referências agora limita cada item a dois segundos e continua após erro, dentro dos dez segundos da iteração. O teste bloqueia a primeira carteira no PostgreSQL e exige que a segunda pendência seja processada. Erros de retomada e HTTP 503 registram os identificadores disponíveis, sem corpo financeiro ou mensagem interna de erro.

## Provas adicionadas ou fortalecidas

| Garantia | Teste |
| --- | --- |
| HTTP e SQS realmente sobrepostos | `HTTPAndSQSOverlapUnderWalletLock`: mantém lock administrativo, exige duas conexões esperando esse lock, libera e confirma uma operação, um débito, inbox e outbox sem duplicação |
| Dois publishers concorrentes | `TestConcurrentClaims`: início sincronizado, dois lotes de dez eventos disjuntos com tokens diferentes; eventos com lease ativo não reaparecem |
| Lease expirado e worker antigo | `TestStorageAudit/LeaseRecoveryAndStaleWorkerFencing`, mantido: retomada conserva ID/payload; confirmação e retry com token antigo não alteram o novo lease |
| Evento publicado antes do crash | `TestPublishBeforeConfirmationCrash`: processo encerra após send, outro retoma; consulta comprova nova tentativa e observer lê a fila real comparando payload, eventId, MessageDeduplicationId e MessageGroupId |
| Reversões simultâneas | `RefundAndRollbackRace`: REFUND e ROLLBACK por processos diferentes; um PROCESSED, outro ALREADY_REVERSED, saldo/versão/ledger reconciliados |
| Replay da disputa 80/80 | `Two80BetsAcrossProcesses`: reenvia ambas as apostas e exige os resultados originais, saldo 20.00 e um único débito |
| Contratos HTTP | `DuplicateOpeningAndMissingKey`: abertura duplicada 409 e chave ausente 400, sem transação adicional |
| Paginação | `TestHTTPBoundaryAudit/PaginationBoundariesAndIsolation`, mantido: percorre páginas sem repetição, rejeita cursor inválido e de outra carteira |
| Contratos sem infraestrutura | `TestDecodeMessageContract`, `TestHTTPErrorContractAndDiagnosticIDs`, `TestModuleGraphWithoutInfrastructure` |
| Tempo e progresso de pendências | `TestStoredFutureTimestamps`, `TestAvailableReferenceWinsAtRetryBoundary`, `TestBlockedReferenceDoesNotStopBatch` |

A deduplicação FIFO pode suprimir o segundo envio dentro da janela do broker. O teste de crash comprova a repetição da tentativa e a identidade/conteúdo do evento recebido; não alega observar duas entregas durante essa janela.

## Decisões documentadas

- Carteira inexistente e jogador divergente não criam uma rejeição financeira persistida. HTTP responde 404/422; SQS faz rollback e redrive enquanto o erro persistir.
- A outbox mantém retries sem limite de tentativas, com backoff limitado a 32 segundos. Atraso e quantidade pendente precisam de monitoramento; o projeto expõe as métricas, sem serviço externo de alertas.
- SIGTERM continua cancelando o processamento e tentando liberar a visibilidade de mensagens sem confirmação. É a política existente, aceita pelo enunciado e coberta pelos ciclos de parada; não foi introduzida outra estratégia de drenagem.

## Ajustes do próprio harness

As rodadas iniciais encontraram fragilidades no harness. A prova HTTP/SQS inicialmente contava somente bloqueios diretos; o PostgreSQL pode enfileirar o segundo solicitante atrás do primeiro. A consulta agora percorre a cadeia de bloqueios a partir da conexão administrativa e exige dois solicitantes nessa cadeia. A execução isolada passou, mas a repetição completa mostrou que o envio simultâneo ainda dependia de uma recepção SQS não ficar invisível após o encerramento anterior. A versão final mantém a carteira bloqueada, aguarda o SQS chegar ao banco, inicia o HTTP e exige ambos simultaneamente na cadeia de espera antes de liberar. Assim separa o prazo de entrega do prazo de processamento e preserva a prova de concorrência.

A espera de 45 segundos pelo redrive da mensagem conflitante não cobria uma recepção perdida (30 segundos de visibilidade) somada aos backoffs das cinco entregas. O teste passou a permitir 90 segundos; continua exigindo a mensagem exata na DLQ e ausência de débito adicional. O timeout geral da suíte continua em dez minutos.

A versão antiga de govulncheck instalada na máquina falhou ao carregar tipos do módulo. A execução da versão atual, com `GOTOOLCHAIN=go1.27.1 go run golang.org/x/vuln/cmd/govulncheck@latest ./...`, terminou com `No vulnerabilities found`.

## Execução

- `scripts/clean-check.sh` em cópia nova, projeto Compose separado e volumes vazios: **passou**. Integração completa com race detector em **308,358 s**; pacote platform com ciclo de vida Fx em **1,041 s**. Incluiu os novos cenários, os três crashes reais, indisponibilidade de PostgreSQL/broker e migrations.
- Testes unitários com `-race`, `go vet ./...`, `go vet -tags=integration ./...`, formatação, `git diff --check`, módulos e configuração Compose: passaram. O enunciado original continua idêntico.
- Build da imagem e três réplicas no ambiente descartável: healthy; smoke autenticado passou. O script removeu somente os containers e volumes descartáveis.
- Imagens principais atualizadas: três réplicas healthy; smoke passou com saldo **85.00**, **cinco lançamentos** e reconciliação consistente. PostgreSQL, Keycloak e broker principais mantiveram seus dados.
- `govulncheck` atualizado: nenhuma vulnerabilidade conhecida encontrada.

Comando da execução completa final: `VERIFY_POSTGRES_PORT=55433 VERIFY_SQS_PORT=54567 VERIFY_KEYCLOAK_PORT=58081 ./scripts/clean-check.sh`. Log local: `work/review/verified-clean.log`. As rodadas iniciais com falhas do harness permanecem em `clean.log` e `final-clean.log`; não são apresentadas como aprovações. Smoke principal: `work/review/smoke-final.log`; plano SQL: `work/review/query-plan.log`.

Esses resultados cobrem os cenários descritos, sem constituir benchmark de capacidade ou garantia de ausência de defeitos. Logs locais ficam em `work/review/` e não entram no pacote de fontes.
