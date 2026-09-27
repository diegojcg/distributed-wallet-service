# Jungle Wallet Service

Serviço Go para processamento distribuído de apostas, com dinheiro exato, PostgreSQL, OIDC, inbox/outbox e SQS. O enunciado original está em [docs/CHALLENGE.md](docs/CHALLENGE.md). Decisões e limitações estão em [ARCHITECTURE.md](ARCHITECTURE.md); andamento em [PLAN.md](PLAN.md). A matriz de cobertura está em [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md) e a evidência de execução em [docs/VALIDATION.md](docs/VALIDATION.md).

## Pré-requisitos

- Docker Engine e Docker Compose v2 com suporte a `--wait` (validado com Engine 25.0.2 / Compose 2.19.1).
- Go 1.27.1 para desenvolvimento/testes. O `go.mod` seleciona essa versão; Go com auto-download de toolchain pode baixá-la sem alterar a instalação global.
- Compilador C para `go test -race` (GCC/Clang).
- Portas locais 55432 (PostgreSQL), 54566 (SQS) e 58080 (Keycloak) disponíveis.

As imagens e dependências têm versões explícitas; `go.sum` é versionado. O ambiente usa apenas dados e credenciais de desenvolvimento, vinculando portas ao loopback. Não reutilize esses exemplos em produção.

## Inicialização completa

```sh
docker compose up --build --scale app=3 -d --wait
docker compose ps
docker compose port --index 1 app 8080
```

Cada réplica recebe uma porta local livre. `docker compose up --build` sem scale também funciona com uma instância. Migrações, realm/clients do Keycloak, filas FIFO/DLQ e usuários/políticas IAM são provisionados automaticamente. `broker-init` falha se as verificações de allow/deny não se comportarem como esperado.

Keycloak: http://localhost:58080 — console local com `local-admin` / `local-admin-only`. O issuer da API é `http://localhost:58080/realms/jungle`, audience `jungle-wallet`.

```sh
BASE="http://$(docker compose port --index 1 app 8080)"
curl "$BASE/health/live"
curl "$BASE/health/ready"
```

Health checks são públicos. Os endpoints de negócio e `/metrics` exigem token.

## Desenvolvimento e testes

```sh
make infra             # containers, IAM, credenciais locais em work/, migrations
cp .env.example .env   # valores locais, arquivo ignorado pelo Git
set -a
. ./.env
set +a
go run ./cmd/server    # http://127.0.0.1:58000
```

`work/*.json` contém exclusivamente credenciais IAM locais, geradas pelo bootstrap. Não versionar. Para desenvolver com containers app já em execução, lembre que eles também consomem as filas; para testes de interrupção determinísticos, pare as réplicas antes da suíte.

```sh
go test ./...
go test -race ./...
go vet ./...
go mod verify
GOTOOLCHAIN=go1.27.1 go run golang.org/x/vuln/cmd/govulncheck@latest ./...

docker compose stop app  # quando houver réplicas em execução
make infra
make integration
```

A suíte de integração usa a build tag `integration`, infraestrutura real, binários com `-race` e três processos com pools/memória separados. Executa testes de autenticação, concorrência, idempotência, reversões, referências pendentes, constraints, outbox, inbox, DLQ, migrações up/down/up em banco temporário e ciclo de vida Fx. Não limpa o banco de desenvolvimento: cria IDs exclusivos por teste. Os bancos temporários de migrations são removidos ao final.

Comando equivalente completo para integração: `scripts/integration.sh`. O script configura URLs locais, serializa os pacotes de teste e aplica timeout de dez minutos. A suíte completa leva alguns minutos por causa dos leases/visibility timeouts reais. Os testes de indisponibilidade param e reativam PostgreSQL/SQS **deste projeto Compose**; execute em ambiente local dedicado, sem tráfego de outras tarefas. O cenário de divergência usa uma conexão administrativa para introduzir e restaurar corrupção em uma carteira exclusiva do teste.

## Tokens e chamadas

Clientes locais: `wallet-internal`, `provider-a`, `provider-b`, `metrics-reader`. Os clientes adicionais `expired-token` (validade de um segundo) e `wrong-audience` servem aos testes negativos de autenticação. O secret de cada um é `<clientId>-local-secret`. Tokens expiram em 120 segundos; obtenha outro quando necessário. Os exemplos abaixo usam Python 3 apenas para extrair JSON.

```sh
TOKEN_INTERNAL=$(curl -fsS http://localhost:58080/realms/jungle/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=wallet-internal \
  -d client_secret=wallet-internal-local-secret | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
TOKEN_PROVIDER=$(curl -fsS http://localhost:58080/realms/jungle/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=provider-a \
  -d client_secret=provider-a-local-secret | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')

curl -sS "$BASE/wallets" -H "Authorization: Bearer $TOKEN_INTERNAL" \
  -H 'Content-Type: application/json' \
  -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"100.00","currency":"BRL"}}'
```

Copie o `id` retornado para `WALLET_ID`. Repetir a abertura com o mesmo jogador/moeda retorna 409.

```sh
WALLET_ID='<id retornado>'
curl -sS "$BASE/wagering/transactions" \
  -H "Authorization: Bearer $TOKEN_PROVIDER" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: provider-a:demo-bet-1' \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"demo-bet-1\",\"playerId\":\"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"demo-round\",\"gameId\":\"demo-game\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}"

curl -sS "$BASE/wallets/$WALLET_ID/ledger?limit=50" -H "Authorization: Bearer $TOKEN_INTERNAL"
curl -sS -X POST "$BASE/wallets/$WALLET_ID/reconciliation" -H "Authorization: Bearer $TOKEN_INTERNAL"
curl -sS "$BASE/providers/provider-a/wagering/transactions/demo-bet-1" -H "Authorization: Bearer $TOKEN_PROVIDER"
```

Repita a aposta com o mesmo corpo/chave: `idempotentReplay=true`, sem outro débito. O saldo retornado é o observado na primeira execução. Contratos, códigos HTTP e permissões estão em ARCHITECTURE.md.

## SQS e eventos

Bootstrap: `infra/ministack/bootstrap.py`. Fila principal `wager-transactions.fifo` pertence ao provider-a; provider-b tem fila própria para impedir falsificação do providerId. Cada uma possui DLQ, visibility de 30 segundos e maxReceiveCount=5. Eventos de saída vão para `wallet-events.fifo`.

Exemplos de entrada/saída e regras de consumo estão em [docs/EVENTS.md](docs/EVENTS.md). `data.idempotencyKey` é obrigatória. Envie com `MessageGroupId=walletId` e `MessageDeduplicationId=messageId`. Credenciais locais específicas por papel ficam em `work/provider-a.json`, `work/provider-b.json`, `work/worker.json` e `work/observer.json` após `make infra`. Use endpoint `http://localhost:54566` e região `us-east-1` com o SDK/CLI SQS.

O teste `TestService/SQSAndHTTPIdempotency` demonstra envio pelo SDK, dez recebimentos efetivos da mesma operação e conflito de inbox enviado à DLQ. Eventos devem ser deduplicados pelo consumidor usando eventId, e projeções de saldo devem considerar walletVersion.

## Migrações

As migrations SQL são incorporadas no binário. `migrate up` é idempotente e serializado por advisory lock. `down` reverte todas as versões instaladas em ordem inversa e **remove todas as tabelas financeiras e seus dados**; use exclusivamente em ambiente descartável e com a aplicação parada.

```sh
# Aplicar todas as versões pendentes:
docker compose run --rm migrate up

# Reverter em um ambiente descartável:
docker compose stop app
docker compose run --rm migrate down

# Aplicar novamente:
docker compose run --rm migrate up
```

Execução local equivalente: `DATABASE_URL='<URL do usuário de migrations>' go run ./cmd/migrate up` ou `down`. A aplicação não usa o usuário administrativo.

## Interrupções reproduzíveis

```sh
./scripts/integration.sh -run TestHTTPCommitCrash -v
./scripts/integration.sh -run TestSQSCommitBeforeACKCrash -v
./scripts/integration.sh -run TestPublishBeforeConfirmationCrash -v
```

Os testes compilam `-tags=failpoints`; a imagem normal não inclui esses pontos de encerramento abrupto. As falhas simuladas são depois do commit HTTP, depois do commit SQS/antes do ACK e depois da publicação/antes da confirmação da outbox. O arquivo marcador garante uma única interrupção, permitindo que outra instância conclua a recuperação.

## Encerramento e dados locais

```sh
docker compose down
```

Esse comando preserva volumes nomeados. O PostgreSQL guarda os dados financeiros. MiniStack usa persistência de estado em volume no encerramento normal; não se afirma equivalência de durabilidade com AWS SQS diante de crash abrupto do próprio emulador. Keycloak é de desenvolvimento e importa o realm em um container novo; recriá-lo invalida tokens antigos.

## Verificação em ambiente limpo

```sh
make clean-check
```

O script copia apenas os fontes para uma pasta temporária em work/, cria um projeto Compose exclusivo e volumes vazios, provisiona IAM/Keycloak/PostgreSQL, executa testes com race detector e sobe a imagem final com três réplicas. Remove apenas os volumes e containers que acabou de criar. A pasta copiada permanece para inspeção. Usa as portas 55433, 54567 e 58081; substitua com VERIFY_POSTGRES_PORT, VERIFY_SQS_PORT e VERIFY_KEYCLOAK_PORT se necessário. Requer também tar, curl, mktemp e Python 3 para o smoke autenticado.

As variáveis de aplicação estão em `.env.example`: DATABASE_URL, OIDC_ISSUER, OIDC_JWKS_URL, OIDC_AUDIENCE, HTTP_ADDR, SQS_ENDPOINT e BROKER_CREDENTIALS_FILE. Em Compose/scripts, POSTGRES_PORT, SQS_PORT e KEYCLOAK_PORT alteram as portas do ambiente; os defaults são 55432, 54566 e 58080. A aplicação recebe credenciais do worker por arquivo montado somente para leitura. Não utiliza as credenciais administrativas do bootstrap.

Os requisitos obrigatórios e os limites da solução estão documentados em ARCHITECTURE.md e docs/REQUIREMENTS.md. Publicar o repositório e enviar o link ao recrutamento são passos externos à execução local.

## Smoke autenticado e auditoria por camada

Com as imagens já em execução, rode `python3 scripts/smoke.py` (Python 3 padrão, sem dependências extras). O script descobre as portas das três réplicas, obtém tokens locais e confere os cinco tipos externos, isolamento, replay histórico, paginação e reconciliação. Ele cria uma carteira de teste com UUID próprio e imprime seu ID; não apaga dados nem reinicia serviços. Para uma única instância use `SMOKE_BASE=http://127.0.0.1:<porta>`; `OIDC_ISSUER` pode substituir o issuer local.

`go test ./internal/domain -fuzz=FuzzMoneyRoundTrip -fuzztime=20s` explora a serialização monetária. Para repetir apenas a auditoria de fronteiras e SQL, após provisionar um ambiente de testes dedicado, execute `./scripts/integration.sh -run 'TestHTTPBoundaryAudit|TestStorageAudit' -v`. O segundo teste usa um banco temporário e diferencia as restrições do usuário da aplicação dos triggers que também bloqueiam alterações pelo administrador.

Consulte `docs/AUDIT.md` para a evidência adicional e `docs/MANUAL.md` para o roteiro de validação manual.

Para uma regressão isolada após uma mudança pontual: `./scripts/clean-check.sh -run 'TestHTTPBoundaryAudit|TestStorageAudit'`. Sem argumentos, o script executa a integração completa. Nos dois casos, a imagem final recebe o smoke autenticado.
