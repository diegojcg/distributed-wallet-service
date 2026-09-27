# Plano de execução

Cronograma adaptado conforme os testes reais. O prazo informado pelo recrutamento é de três dias desde o envio do convite; a data do convite não foi fornecida.

## Etapa 1 — infraestrutura e fluxo financeiro

- [x] Enunciado original preservado em docs/CHALLENGE.md; inspeção de segurança fora da entrega.
- [x] Go Modules, Dockerfile, Compose e migrations reversíveis.
- [x] PostgreSQL com identidade de aplicação separada, Keycloak e MiniStack com IAM efetivo.
- [x] Fluxos HTTP/SQS, precisão monetária, lock por carteira, ledger, inbox e outbox.
- [x] Harness de três processos, disputa 80/80 e 50 duplicatas.
- [x] Interrupções reais após commit HTTP, commit SQS e publicação de evento; start/stop Fx.

## Etapa 2 — contratos e recuperação

- [x] Wallet, WagerTransaction e WalletLedgerEntry encapsuladas, criação/reidratação separadas e eventos concretos imutáveis.
- [x] Falha permanente auditada como FAILED após rollback; erros transitórios continuam recuperáveis.
- [x] Métricas de resultados, replay, retries, DLQ, conflitos, atraso da outbox, latência e divergência.
- [x] correlationId/causationId propagados e preservados na retomada; contratos em docs/EVENTS.md.
- [x] Token expirado/audience incorreta reais, isolamento sem efeitos financeiros, OPENING externo, zeros, overflow, referências rejeitadas e expiração.
- [x] Falhas permanentes, inbox atômica, falsificação do corpo SQS e redrive para DLQ.
- [x] Reinício das três instâncias, indisponibilidade de PostgreSQL/SQS e oito carteiras em paralelo.
- [x] Cancelamento, encerramento de clientes/pool e classificação dos erros revisados.
- [x] Proteções adicionais para identidade/versão da carteira e unicidade/contexto do ledger.

## Etapa 3 — evidência e entrega

- [x] Matriz de requisitos → implementação → teste em docs/REQUIREMENTS.md.
- [x] Execução completa em cópia limpa, volumes novos, provisionamento automático e três réplicas finais.
- [x] Revisão de permissões, variáveis e dependências; govulncheck sem vulnerabilidades conhecidas.
- [x] Validação final de formatação, testes/race, vet, módulos e migrations após os últimos ajustes.
- [x] Evidências finais, revisão do diff e pacote de fontes para entrega.

## Ajustes feitos durante a execução

A infraestrutura e o harness foram antecipados para verificar SQL e IAM desde o início. O MiniStack retorna a conta em SenderId, e não o usuário IAM; cada provedor passou a ter sua própria fila protegida. A fila principal exigida foi preservada para provider-a.

A migração dos metadados de correlação drena os triggers diferidos antes de alterar a tabela novamente. O teste SQS contempla uma resposta de long polling perdida após interrupção: a espera permite o visibility timeout de 30 segundos mais a janela de recepção, sem remover as verificações de inbox/debito único.

A política de reversão permite apenas uma reversão bem-sucedida por referência, mesmo entre REFUND e ROLLBACK. Não há commit intermediário de PENDING; PENDING_REFERENCE é retomável pelo banco. FAILED nunca é usado para timeout ou commit ambíguo.

Publicação do repositório e envio de e-mail ficam fora da execução local. Diferenciais opcionais não implementados: partidas dobradas, tracing, dashboard e benchmark de capacidade com percentis.

A revisão independente levou às correções de relógio, expiração de referências e consulta por UUID, além de novas provas de concorrência e recuperação. Decisões e execução estão em docs/REVIEW-FIXES.md.
