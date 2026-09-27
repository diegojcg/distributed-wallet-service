# Matriz de requisitos e evidências

O texto original está em CHALLENGE.md. Caminhos abaixo são relativos à raiz do repositório. Os testes de integração rodam contra PostgreSQL, Keycloak e MiniStack reais; `setup` compila e executa três processos independentes com race detector. Um teste unitário não é apresentado como prova de uma integração.

| Requisito do enunciado | Implementação | Evidência executável |
| --- | --- | --- |
| Go, módulos e versões (§4) | go.mod, go.sum, Dockerfile, compose.yaml | go mod verify; make clean-check |
| OIDC externo e identidade do provedor (§2) | internal/auth; realm Keycloak; middleware HTTP | TestService/AuthAndProviderIsolation; TestContracts/RealExpiredTokenAndAudience |
| Isolamento de consultas/replays e operações internas (§2, §9) | Filtro provider_id, claims service_role/provider_id | TestService/AuthAndProviderIsolation; negativa sem efeito em TestContracts |
| IAM no broker (§2, §10) | infra/ministack/bootstrap.py, fila por provedor | Bootstrap allow/deny; SQSAndHTTPIdempotency; PermanentFailureAndPoisonMessages |
| Fx e encerramento (§4) | platform.Module, construtores, hooks, contextos/prazos | TestFxLifecycle; stop/restart do harness e testes de crash |
| Domínio independente/encapsulado (§6) | Money, Wallet, WagerTransaction, WalletLedgerEntry; snapshots por valor | TestWalletInvariants; TestTransactionDecisions; TestOverflowOpeningLedgerAndEvents |
| Precisão, escala, moeda e overflow (§5–6) | Money int64, BIGINT; soma de reconciliação NUMERIC exata | TestMoneyParsing, TestMoneyArithmeticBoundaries, TestMoneyJSON, FuzzMoneyRoundTrip; overflow persistido em TestContracts |
| Zero por tipo e OPENING interno (§6–7) | Operation.Validate, NewOpeningTransaction, schema interno/externo | TestOperationNormalization; ExternalOpeningZerosAndOverflow; poison OPENING SQS |
| Abertura atômica, unicidade e versão (§6.2–6.4, §9) | Store.Open; constraints e triggers; versão inicial 1 | TestOverflowOpeningLedgerAndEvents; DatabaseGuards; abertura positiva/zero e reconciliações |
| Regras de BET/WIN/LOSS (§7) | WagerTransaction.Evaluate e Wallet | TestTransactionDecisions; ReversalPolicyAndLoss; Two80BetsAcrossProcesses |
| REFUND/ROLLBACK e reversão única (§7) | Evaluate, one_successful_reversal, contexto SQL | TestReversalDecisions; TestReferenceContextAndKind; ReversalPolicyAndLoss |
| Estados e terminais imutáveis (§6.3) | Transições do domínio, guard_transaction | TestPendingAndFailureStates; DatabaseGuards |
| FAILED auditável versus retry transitório (§6.3) | store/failure.go, rollback antes da auditoria | TestPermanentClassification; TestPermanentFailureAndPoisonMessages; TestDependencyOutages |
| Hash canônico, duas unicidades e saldo histórico (§9) | Operation.Hash; replay; constraints por provedor | TestOperationNormalization; 50DuplicatesAndHistoricalReplay; SQSAndHTTPIdempotency |
| Concorrência distribuída e ausência de lost update (§5, §8) | SELECT FOR UPDATE por carteira, UPDATE com versão, schema | Two80BetsAcrossProcesses; 50DuplicatesAndHistoricalReplay, três processos reais |
| Carteiras independentes (§5, §8) | Ausência de lock global no fluxo financeiro | IndependentWallets (uma carteira bloqueada); ParallelIndependentWallets (80 operações/8 carteiras) |
| Ledger append-only e saldo consistente (§5–6) | REVOKE, triggers, unicidades e verificações diferidas; migration 003 | DatabaseGuards; falha SQL após atualização da operação em PermanentFailureAndPoisonMessages; reconciliações |
| Atomicidade domínio/inbox/outbox (§6.5, §10–11) | Store.run com uma transação SQL; auditoria FAILED independente após rollback | SQSAndHTTPIdempotency; SQSCommitBeforeACKCrash; PermanentFailureAndPoisonMessages |
| Pendência, backoff, TTL e retomada (§7) | store/references.go + Evaluate; metadados persistidos | PendingReferenceRecovery; ReferenceExpiryAndRejectedReference; AllProcessesRestart |
| Inbox conflituosa e deduplicação efetiva (§10) | Hash integral do envelope, PK consumer/messageId | Dez mensagens recebidas em SQSAndHTTPIdempotency; alteração do mesmo messageId chega à DLQ |
| ACK após commit, redrive e liberação (§10) | workers.handleMessage, visibility 30s/processamento 10s/redrive 5 | TestSQSCommitBeforeACKCrash; poison/permanent failures; ciclos de stop |
| Outbox concorrente, lease e retry (§11) | Claim SKIP LOCKED, lease 30s e token de confirmação | OutboxPublishedByConcurrentWorkers; PublishBeforeConfirmationCrash; BrokerUnavailableKeepsCommittedEvents |
| Eventos tipados e payload imutável (§11) | domain/events.go; guard_outbox; docs/EVENTS.md | TestOverflowOpeningLedgerAndEvents; CorrelationAndImmutableEventSnapshot |
| Idempotência após perda da resposta (§3, §13) | Resultado persistido antes de responder | TestHTTPCommitCrash |
| Reinício de todas as instâncias (§3, §13) | Estado financeiro, pendências e eventos no PostgreSQL | AllProcessesRestart; TestDependencyOutages com stop/restart total |
| Contrato HTTP e paginação (§9) | platform/http.go; store/ledger.go; ARCHITECTURE.md | TestService; TestContracts; validação de cursor/limite no handler/store |
| Reconciliação consistente e sem reparo (§9) | REPEATABLE READ READ ONLY, cálculo exato, log/contador | Two80BetsAcrossProcesses; ParallelIndependentWallets; ReconciliationReportsDriftWithoutRepair |
| Health público e dependências (§9) | Ping PostgreSQL e consulta autenticada SQS | TestFxLifecycle; TestDependencyOutages; health das três réplicas finais |
| Observabilidade (§12) | Logs JSON, métricas protegidas, correlação persistida | CorrelationAndImmutableEventSnapshot; ReconciliationReportsDriftWithoutRepair; métricas no teste de outage |
| Migrações reversíveis e checkout limpo (§4, §15) | migrations 001–003; scripts/clean-check.sh | TestMigrationRollback (up/down/up); make clean-check |
| Formatação, race detector e análise (§13, §15) | Comandos em README/Makefile | gofmt, go test, go test -race, go vet, govulncheck; docs/VALIDATION.md |

## Interpretações e limites

Uma referência admite uma única reversão bem-sucedida, mesmo entre tipos diferentes. Rollback de refund é permitido, sem liberar outra reversão da aposta original. Há processamento síncrono sem commit intermediário de PENDING; somente PENDING_REFERENCE é confirmado para retomada. FAILED não é uma rejeição de negócio e não emite WagerTransactionRejected.

Os testes paralelos verificam correção, não constituem benchmark de capacidade. Não foram implementados partidas dobradas, dashboard, OpenTelemetry ou resultados de carga com percentis; são diferenciais opcionais. Não se alega equivalência de durabilidade entre MiniStack e AWS SQS nem configuração pronta para produção. As limitações operacionais estão em ARCHITECTURE.md.
