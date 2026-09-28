# Testes no Postman

Importe o [environment local](Jungle-Wallet-local.postman_environment.json) e uma ou ambas as collections:

- [Roteiro manual](Jungle-Wallet.postman_collection.json): 28 chamadas ordenadas, incluindo abertura, cinco tipos de operação, replay, paginação, reconciliação e erros de contrato/autorização.
- [Concorrência HTTP](Jungle-Wallet-concorrencia.postman_collection.json): cinco cenários independentes com disparos paralelos e verificação do estado financeiro.

## Configuração

Na raiz do repositório, inicie a aplicação e descubra as portas:

```sh
docker compose up --build --scale app=3 -d --wait
docker compose port --index 1 app 8080
docker compose port --index 2 app 8080
docker compose port --index 3 app 8080
```

Selecione **Jungle Wallet local** no Postman e ajuste `baseUrl` para a primeira URL, incluindo `http://`. O valor inicial `http://127.0.0.1:58000` corresponde à execução direta com Go; as portas do Compose são dinâmicas e precisam ser conferidas após reiniciar os containers. `keycloakUrl` usa `http://localhost:58080` por padrão.

As chamadas protegidas obtêm tokens novos automaticamente via client_credentials. O environment contém os secrets demonstrativos dos clients locais, sem tokens exportados.

## Roteiro manual

Execute os itens 00–27 em ordem, com **Send** ou **Run collection**. A requisição 02 inicia outra carteira, gerando jogador/IDs únicos e salvando-os nas variáveis da collection. Não sobreponha essas variáveis com versões antigas no environment.

O estado final esperado é saldo **85.00**, versão **5**, **cinco lançamentos** e reconciliação consistente. O replay do BET retorna o saldo histórico **75.00**, sem mudar o saldo atual. Nos testes negativos, os códigos 400, 401, 403, 404, 409 e 422 são resultados esperados. Para reiniciar o roteiro, execute novamente o item 02 e siga a ordem.

## Concorrência

Preencha `replicaUrls` na collection de concorrência com as URLs das três instâncias separadas por vírgula. Se também existir no environment, o valor dele tem prioridade. Quando vazia, essa variável faz o roteiro usar apenas `baseUrl`.

Use **Run collection**, selecione os cinco itens, **Delay = 0** e comece com uma iteração; depois repita com três ou cinco. Cada cenário também pode ser executado separadamente por **Send** e cria seus próprios dados. Os IDs das carteiras aparecem no Console.

| Cenário | Resultado esperado |
| --- | --- |
| Duas BETs de 80 sobre saldo 100 | Uma aceita, outra rejeitada; saldo 20; reenvios preservam resultados |
| 50 cópias da mesma BET de 25 | Um débito, 49 replays; saldo 75; replay continua histórico após WIN |
| Mesma chave com BET 25 e BET 30 | Uma aceita e uma 409; saldo conforme o vencedor, um único débito |
| REFUND e ROLLBACK da mesma BET | Uma reversão aceita e outra ALREADY_REVERSED |
| 80 BETs em oito carteiras | Dez débitos de 1 por carteira; saldo 90 e versão 11 em cada uma |

O GET de health é o ponto de partida de cada item. O script após a resposta faz preparação, disparos assíncronos via pm.sendRequest/Promise.all e auditoria. Confira **Test Results**, não apenas o HTTP 200 do GET. O Runner percorre os itens em sequência; a concorrência acontece dentro deles. Iterations repete os cenários, sem aumentar o tamanho dos grupos paralelos.

A collection cria 12 carteiras por iteração e preserva os dados. Ela verifica saldo/versão em cada URL, unicidade e soma do ledger e reconciliação. É uma prova de correção sob disputa, sem meta de latência ou alegação de benchmark. A chegada simultânea ao banco não é garantida pelo cliente HTTP; a suíte Go contém a prova de sobreposição HTTP/SQS com locks observados no PostgreSQL.

## Falhas e recuperação

Os testes de crash, SQS e indisponibilidade são executados pela suíte Go em ambiente descartável:

```sh
./scripts/clean-check.sh -run '^(TestHTTPCommitCrash|TestSQSCommitBeforeACKCrash|TestPublishBeforeConfirmationCrash|TestDependencyOutages|TestPermanentFailureAndPoisonMessages)$'
```

Para toda a suíte, execute `make clean-check`. Ela usa outro projeto Compose, volumes novos e portas próprias. Não rode `make integration` no ambiente em que está fazendo testes manuais: essa variante interrompe as dependências do projeto atual.

## Evidência

As collections foram executadas com Newman 6.2.2 contra aplicação, PostgreSQL, Keycloak e broker reais em ambiente separado:

- Roteiro manual: 28 chamadas, 25 requisições auxiliares de autenticação e 76 assertions, zero falhas.
- Concorrência: três iterações distribuídas por três réplicas, 705 requisições incluindo preparação/consultas e 480 assertions, zero falhas. Usando apenas uma URL, as mesmas três iterações verificam 324 assertions.

Documentação oficial: [Collection Runner](https://learning.postman.com/docs/tests-and-scripts/running-collections/intro-to-collection-runs) e [Newman](https://learning.postman.com/docs/reference/newman-cli/installing-running-newman/).
