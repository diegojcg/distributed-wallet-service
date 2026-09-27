# Arquitetura e decisões

## Escopo e estado

Serviço de carteiras em Go 1.27.1, PostgreSQL 17, pgx com SQL explícito, Uber Fx, Keycloak e SQS em MiniStack. O enunciado original está em `docs/CHALLENGE.md`; o acompanhamento de execução e lacunas fica em `PLAN.md`. Não há chamadas para AWS real.

## Dinheiro e domínio

`Money` mantém `int64` em centavos e moeda, sem ponto flutuante. Faixa interna: −92.233.720.368.547.758,08 a 92.233.720.368.547.758,07. Parsing externo rejeita sinais, espaços, notação científica, valores não finitos, frações com mais de duas casas e overflow. Aceita `25`, `025.0` e `25.00`, normalizados para `25.00`. BRL, USD e EUR são reconhecidas pelo value object; a aplicação opera apenas BRL. O zero Go do tipo é inválido. Cálculos internos podem ser negativos; saldo não pode.

Wallet, WagerTransaction e WalletLedgerEntry têm estado privado, snapshots por valor e criação/reidratação separadas. Evaluate contém as regras e transições financeiras sem I/O; o Store coordena persistência e locks. Wallet tem campos privados, criação e reidratação distintas, débito/crédito validados e versão inicial 1. Apenas mudanças de saldo incrementam a versão. Abertura positiva gera OPENING e ledger na versão 1; abertura zero não gera lançamentos. LOSS mantém saldo e versão. IDs UUID são normalizados no hash; identificadores externos são sensíveis a maiúsculas, não sofrem trim silencioso e não admitem espaços nas extremidades.

## Transação SQL e concorrência

O Store é a unidade de trabalho. Nenhum repositório aninhado abre uma transação independente. Cada operação confirma transação de negócio, saldo, ledger, outbox e, em SQS, inbox no mesmo commit. Não há commit intermediário de PENDING para operações sem dependências.

READ COMMITTED + SELECT FOR UPDATE na carteira serializa alterações daquela carteira. Após adquirir o lock, o UPDATE também exige a versão lida. A ordem é carteira antes da alteração do registro da operação. Carteiras distintas não compartilham lock de aplicação. Deadlocks, indisponibilidade e timeout desfazem a tentativa e retornam retry/503; um erro de rede durante COMMIT pode ter resultado ambíguo, resolvido por nova consulta idempotente.

O banco impõe saldo não negativo, integridade referencial, unicidades de transação e lançamento, imutabilidade do ledger e dos estados terminais. Triggers diferidas verificam a relação transação–ledger, direção financeira, continuidade do ledger e igualdade do saldo com a soma dos lançamentos. A aplicação usa `jungle_app`, sem ser dona das tabelas e sem UPDATE/DELETE/TRUNCATE no ledger; migrations usam outra identidade.

**Custo conhecido:** a verificação diferida de consistência percorre o ledger da carteira. É uma defesa forte e verificável para o desafio, mas cresce com o histórico. Antes de produção, medir e substituir por uma estratégia incremental igualmente protegida. Não há meta de throughput assumida.

## Idempotência

Unicidades `(provider_id,idempotency_key)` e `(provider_id,external_id)`. INSERT ON CONFLICT DO NOTHING, seguido de um SELECT separado em READ COMMITTED quando há disputa. Conflito entre chaves apontando para registros diferentes também retorna 409. Uma chave nova para a mesma operação externa e conteúdo retorna o resultado original; a chave alternativa não é registrada como alias persistente.

SHA-256 do JSON emitido a partir de mapas com chaves ordenadas. Campos: providerId, externalTransactionId, playerId, walletId, roundId, gameId, kind, money.amount, money.currency e referenceExternalTransactionId. Valores são strings e objetos; não há números monetários JSON. Ausência de referência normaliza para string vazia, UUIDs para representação padrão e dinheiro para duas casas. Esta é uma canonicalização própria e determinística do contrato, não uma implementação genérica de RFC 8785.

Não entram no hash de negócio: chave de idempotência, messageId, occurredAt ou metadados HTTP/SQS. Replay terminal devolve o saldo persistido no processamento original. Rejeições também são persistentes. A inbox separadamente aplica SHA-256 aos bytes completos do envelope: alteração de qualquer byte com o mesmo messageId é conflito de mensagem, sem novo efeito financeiro.

## Estados e referências

PENDING → PROCESSED, REJECTED, PENDING_REFERENCE ou FAILED. PENDING_REFERENCE → PENDING_REFERENCE ou estado terminal. Estados terminais são imutáveis. O construtor de estado e o schema rejeitam transições inválidas.

Referência ausente ou ainda pendente gera PENDING_REFERENCE durável, com um único evento de entrada nessa condição. Worker consulta pendências e coordena a retomada pelo mesmo lock de carteira, rechecando estado e prazo dentro da transação. Backoff 1, 2, 4, 8, 16, 32 segundos; máximo de dez retomadas ou cinco minutos. Esgotamento gera REJECTED/REFERENCE_NOT_FOUND. Referência REJECTED ou FAILED gera REFERENCE_UNSUCCESSFUL. Divergência de provedor/jogador/carteira/moeda/rodada impede processamento. WIN com referência exige BET da mesma rodada; o valor de WIN pode diferir da aposta.

Falhas transitórias (conexão, timeout, cancelamento, deadlock e serialização) fazem rollback e retornam 503/retry. Nenhuma tentativa de commit ambíguo vira FAILED. Uma lista explícita de respostas PostgreSQL que abortam a tentativa é considerada permanente: 0A000 (operação não suportada), 22003 (overflow de persistência), 23514 (invariante SQL violada) e 42883 (função ausente). São defeitos de infraestrutura/schema; rejeições financeiras esperadas são resolvidas no domínio antes desses erros.

Após rollback, uma nova transação, com o mesmo lock/idempotência, registra FAILED/INFRASTRUCTURE_PERMANENT e o saldo observado. Em SQS, a inbox é confirmada junto com essa auditoria. Se nem essa escrita for possível, permanece 503/retry; não se inventa auditoria confirmada. FAILED não movimenta saldo/ledger nem emite evento financeiro de rejeição. Reenvios retornam o resultado terminal; o consumer deixa a mensagem seguir para DLQ. Publicação de outbox jamais reclassifica uma transação financeira já confirmada.

## Reversões

Índice único parcial em resolved_reference_id para PROCESSED e tipo REFUND/ROLLBACK: cada referência admite uma única reversão bem-sucedida, independentemente do tipo. REFUND só referencia BET. ROLLBACK referencia BET, WIN ou REFUND. Valor integral e contexto financeiro precisam coincidir.

ROLLBACK de REFUND debita o crédito devolvido. Isso não libera a aposta original para outra reversão: a política é deliberadamente conservadora. Não se permite ROLLBACK de ROLLBACK. Falta de saldo para aposta gera INSUFFICIENT_FUNDS; para reversão gera REVERSAL_INSUFFICIENT_FUNDS. Outros códigos: ALREADY_REVERSED, REFERENCE_MISMATCH, REFERENCE_KIND_INVALID, REFERENCE_AMOUNT_MISMATCH, REFERENCE_UNSUCCESSFUL, REFERENCE_NOT_FOUND e BALANCE_OVERFLOW.

## Autenticação e autorização

Keycloak provisiona clientes de serviço com client_credentials. A API valida assinatura RS256 via JWKS, issuer, audience e expiração usando go-oidc. Nenhum token próprio é emitido. Claims `service_role` e `provider_id` são mappers fixos do IdP.

| Operação | Identidade exigida |
| --- | --- |
| POST/GET carteiras, ledger, reconciliação | service_role=internal |
| Enviar/consultar transações externas | service_role=provider e provider_id |
| GET /metrics | service_role=metrics |
| GET /health/live e /health/ready | Público |

O provider do corpo/path deve coincidir com o token. Consultas por ID filtram pelo provedor e retornam 404 para dados de outro. Não há privilégio implícito de consulta de transações externas para o serviço interno. A URL pública do issuer é fixa; o JWKS usa a rede interna no Compose, sem desabilitar validação de issuer/audience.

## Broker e isolamento de provedores

MiniStack 1.5.17 com AUTH=true. Bootstrap cria usuários/políticas e executa testes reais de allow/deny antes de permitir que a aplicação suba. O worker lê entradas e publica eventos; não publica comandos. Cada produtor só envia para sua própria fila e não pode consumir. No volume montado, o processo app (UID 10001) lê somente worker.json; credenciais de produtores/observer pertencem ao root com modo 0600. O worker.json pertence ao UID 10001 com modo 0400. A cópia local em work/ usa modo 0600.

A inspeção prática mostrou que SenderId no MiniStack devolve a conta em vez do UserId IAM. Portanto, a identidade não é deduzida desse atributo nem confiada ao corpo: cada fila de entrada é vinculada a um provedor na configuração provisionada.

- provider-a: `wager-transactions.fifo` → `wager-transactions-dlq.fifo`.
- provider-b: `provider-b-wager-transactions.fifo` → `provider-b-wager-transactions-dlq.fifo`.
- Saída: `wallet-events.fifo`, leitura reservada à identidade observer.

SQS usa MessageGroupId=walletId e MessageDeduplicationId=messageId no envio normal. O consumer verifica se providerId corresponde à fila. Nos testes, IDs de deduplicação de transporte distintos forçam recebimentos reais da mesma operação, evitando que a deduplicação FIFO esconda bugs.

Visibility timeout de 30 segundos, processamento limitado a 10 segundos, long polling de 10 segundos e maxReceiveCount=5. Retry de mensagem usa backoff de visibilidade 2, 4 e 8 segundos, limitado a 8. Mensagens inválidas/conflitantes não são confirmadas e chegam à DLQ pelo redrive do broker. Em SIGTERM, o trabalho é cancelado; mensagens sem confirmação tentam liberar a visibilidade com timeout de limpeza independente. Se a liberação falhar, a visibilidade expira naturalmente. Mensagem removida apenas após commit da inbox/domínio.

## Outbox

Claim atômico por UPDATE ... FROM SELECT FOR UPDATE SKIP LOCKED, até dez eventos, lease de 30 segundos e token UUID. A publicação ocorre fora da transação financeira. A confirmação exige o token atual: worker antigo não pode confirmar lease de outro. Falhas recebem backoff; lease expirado permite retomada por outra instância.

Eventos preservam eventId ao republicar. Envelope contém eventId, eventType, aggregateId, correlationId, occurredAt UTC, version e data tipado. A correlação vem de X-Correlation-ID em HTTP ou correlationId/messageId em SQS; causationId identifica a mensagem SQS. Metadados são persistidos e preservados na retomada. Contratos completos e exemplos estão em `docs/EVENTS.md`. Payload imutável no schema. A fila de saída recebe todos os tipos; consumidores roteiam por eventType e deduplicam persistentemente por eventId.

Não se promete exactly-once nem ordem global. Publishers concorrentes podem publicar eventos de uma carteira fora da ordem financeira; consumidores que mantêm uma projeção devem usar walletVersion e reconciliar lacunas. O ledger continua sendo a fonte financeira de verdade.

## HTTP e reconciliação

| Situação | HTTP |
| --- | --- |
| Carteira criada | 201 |
| Processado/replay bem-sucedido/consulta | 200 |
| Referência pendente | 202 |
| Entrada inválida | 400 |
| Token ausente/inválido/expirado | 401 |
| Identidade sem permissão | 403 |
| Recurso não encontrado/isolado | 404 |
| Conflito idempotente/carteira duplicada | 409 |
| Rejeição de negócio | 422 |
| Falha permanente auditada (FAILED) | 500, resultado persistido e failureCode |
| Indisponibilidade transitória | 503 + Retry-After: 1 |

Rejeições financeiras retornam transactionId, status, failureCode, saldo observado e idempotentReplay. Erros de transporte retornam `{ "code": "..." }`. A reconciliação usa REPEATABLE READ READ ONLY, incluindo abertura; diferença = saldo armazenado − soma do ledger. Não altera dados. Divergência é registrada em log e contador.

Ledger paginado por versão crescente, cursor base64url ligado à carteira e última versão, limite padrão 50/máximo 100. O cliente deve tratar o cursor como opaco.

## Ciclo de vida e falhas

Fx injeta configuração, pool, broker, store, auth, métricas, workers e HTTP. Hooks validam PostgreSQL, JWKS e filas antes de servir. Shutdown cancela a busca e o trabalho dos workers, drena o HTTP, aguarda os workers e então fecha pool e conexões ociosas dos clientes HTTP. Falha inesperada do servidor dispara shutdown do Fx. Nenhum worker utiliza um pool já fechado.

Builds normais não contêm os failpoints. `-tags=failpoints` habilita encerramento abrupto nos pontos after_http_commit, after_commit_before_ack e after_publish. Um arquivo marcador por execução permite uma única interrupção e retomada por outra instância.

## Observabilidade

Logs JSON registram correlação e os IDs disponíveis, estado e código de falha; não incluem tokens, secrets ou corpos financeiros completos. `/metrics` exige a identidade metrics e oferece:

| Métrica | Uso |
| --- | --- |
| wallet_transactions_total{status,transport} | Respostas HTTP/SQS e transições pelo worker, incluindo replay nas entradas |
| wallet_idempotent_replays_total | Replays HTTP/SQS |
| wallet_retries_total{component} | Tentativas repetidas/erros de workers e atualização das métricas |
| wallet_concurrency_conflicts_total{reason} | Conflito idempotente, deadlock e falha de serialização |
| wallet_dlq_messages{provider} | Aproximação de mensagens visíveis/em voo na DLQ |
| wallet_outbox_oldest_seconds / wallet_outbox_pending | Atraso e quantidade de eventos pendentes |
| wallet_pending_references | Pendências de referência persistidas |
| wallet_processing_seconds | Histograma de duração do caso de uso |
| wallet_reconciliation_divergences_total | Divergências encontradas pela reconciliação |

Labels têm conjuntos controlados; IDs não são labels. Métricas de filas/pendências refletem estado compartilhado e devem ser agregadas por max entre réplicas, não sum. Em falha de atualização, o último gauge conhecido permanece e wallet_retries_total{component="metrics"} aumenta; não se apresenta zero falso. Contadores são locais ao processo. Não há dashboard/tracing ou benchmark de capacidade; esses itens são opcionais.

## Limites e operação

O ambiente é local, usa HTTP e credenciais demonstrativas; não é um template pronto para produção. O broker é um emulador, Keycloak usa start-dev, as tabelas de auditoria não têm política de retenção e a verificação do histórico cresce com o ledger. A moeda operacional é BRL e a política de reversão impede uma segunda reversão da mesma referência, inclusive de outro tipo. Essas são decisões explícitas; nenhum requisito obrigatório permanece marcado apenas como planejado.

## Referências técnicas

- PostgreSQL: https://www.postgresql.org/docs/current/transaction-iso.html
- Keycloak containers: https://www.keycloak.org/server/containers
- MiniStack: https://github.com/ministackorg/ministack/tree/v1.5.17
- SQS deduplication: https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/using-messagededuplicationid-property.html
