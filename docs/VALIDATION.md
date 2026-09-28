# Validação

Evidências das execuções de 27 e 28/09/2026. Ambiente: Linux amd64, Go 1.27.1, Docker Engine 25.0.2 e Compose 2.19.1. Dependências reais: PostgreSQL 17.6, Keycloak 26.7.4 e MiniStack 1.5.17 com AUTH=true.

## Resultados

| Verificação | Resultado |
| --- | --- |
| `go test ./...` e `go test -race ./...` | Passaram |
| `go vet ./...` e `go vet -tags=integration ./...` | Passaram |
| `go mod verify` | Todos os módulos verificados |
| `gofmt -l cmd internal migrations tests` | Nenhum arquivo sem formatação |
| `GOTOOLCHAIN=go1.27.1 go run golang.org/x/vuln/cmd/govulncheck@latest ./...` | Nenhuma vulnerabilidade conhecida encontrada na execução de 27/09 |
| `scripts/clean-check.sh`, execução completa final | Passou: integração com race detector em 308,358 s; pacote platform com ciclo de vida Fx em 1,041 s |
| Build da imagem e três réplicas em ambiente limpo | Saudáveis; smoke autenticado passou |
| Smoke das três réplicas no ambiente principal | Passou: saldo 85.00, versão 5, cinco lançamentos e reconciliação consistente |
| Permissões dos arquivos IAM dentro das réplicas | Worker legível; provider-a/provider-b/observer inacessíveis ao UID da aplicação |
| `FuzzMoneyRoundTrip`, 20 segundos | 10.229.670 execuções sem falha; não é benchmark do serviço |
| `docker compose config --quiet` e `sh -n scripts/*.sh` | Passaram |
| Comparação de `docs/CHALLENGE.md` com README original | Idênticos |
| Collection Postman manual, Newman 6.2.2 | 28 chamadas, 25 requisições auxiliares de autenticação, 76 assertions e zero falhas |
| Collection Postman concorrente, Newman 6.2.2 | Três iterações em três réplicas: 705 requisições, 480 assertions e zero falhas |

Os resultados acima registram execuções concluídas, não verificações automáticas a cada acesso ao repositório. As collections e as instruções de importação estão em [postman/README.md](postman/README.md).

## Reproduzir em ambiente isolado

```sh
make clean-check
```

O script cria uma cópia de fontes, outro projeto Compose e volumes vazios, provisiona PostgreSQL, filas, políticas/usuários IAM, realm/clients do Keycloak e migrations. Executa testes unitários/race, vet, módulos e integração; depois constrói a imagem final, inicia três réplicas e executa o smoke. Ao terminar, remove somente os containers e volumes desse ambiente descartável.

Portas padrão: 55433, 54567 e 58081. Podem ser substituídas por `VERIFY_POSTGRES_PORT`, `VERIFY_SQS_PORT` e `VERIFY_KEYCLOAK_PORT`. A pasta de fontes copiada permanece em `work/` para inspeção. Logs e credenciais geradas ficam fora do Git.

A suíte usa três processos com pools e memória separados, compilados com `-race`. Os failpoints só existem nas builds de teste; um arquivo marcador comprova que cada interrupção realmente aconteceu. Os testes diretos de storage/migrations usam bancos temporários e conexões distintas para administrador e aplicação.

## Cenários confirmados

| Garantia | Evidência |
| --- | --- |
| OIDC e isolamento | Tokens reais: ausência, inválido, expirado, audience incorreta, roles e isolamento entre provedores; acesso negado sem efeito financeiro |
| Dinheiro exato | Parsing, precisão, overflow, zero por tipo, OPENING interno e rejeição de OPENING externo por HTTP/SQS |
| Concorrência entre instâncias | Disputa 80/80 sobre saldo 100, reenvio de ambas, 50 duplicatas e oito carteiras com 80 operações concorrentes |
| HTTP/SQS sobrepostos | `HTTPAndSQSOverlapUnderWalletLock` exige duas conexões na cadeia de espera do mesmo lock antes de liberá-lo; confirma um débito, uma operação e inbox/outbox sem duplicação |
| Reversões | REFUND e ROLLBACK simultâneos da mesma BET: um processado e outro ALREADY_REVERSED; LOSS não altera saldo/versão |
| Referências pendentes | Retomada, expiração, referência malsucedida, referência disponível na última tentativa/após TTL e lote que continua após uma carteira bloqueada |
| Relógio compartilhado | `TestStoredFutureTimestamps` injeta timestamps futuros distintos na carteira e transação e verifica Apply, Consume, retomada e FAILED |
| Privilégios e imutabilidade | Runtime recebe 42501 ao editar/excluir/truncar ledger ou desabilitar proteções; triggers recusam mutações administrativas com 23514 |
| Integridade SQL | Saldo negativo, saldo sem ledger, identidade/versão inválidas, payload da outbox alterado e ledger associado à carteira errada são recusados |
| Atomicidade | Erro de serialização no INSERT da outbox desfaz inbox, operação, saldo e ledger; falha SQL permanente desfaz efeitos financeiros antes de auditar FAILED |
| Idempotência | Duas unicidades, conflito entre chaves, hash normalizado, saldo histórico, persistência após reinício e inbox conflitante sem débito adicional |
| Outbox concorrente | Dois claims simultâneos recebem lotes disjuntos; eventos com lease ativo não reaparecem |
| Lease e worker obsoleto | Lease expirado é retomado com novo token e mesmos ID/payload; confirmar ou reagendar com token antigo não altera o lease novo |
| Publicação após crash | Observer consome a fila real e compara payload, eventId, MessageDeduplicationId e MessageGroupId com a outbox após retomada |
| Quedas e reinício | Reinício de todas as instâncias; PostgreSQL indisponível retorna erro transitório; broker indisponível preserva eventos para publicação posterior |
| Crashes reais | Encerramento após commit HTTP, após commit SQS/antes do ACK e após publicação/antes da confirmação da outbox; recuperação por outra instância |
| HTTP e paginação | UUIDs equivalentes/nulos, campos obrigatórios, abertura duplicada, chave ausente, percurso completo do ledger, cursor inválido e cursor de outra carteira |
| Consultas por UUID | EXPLAIN da consulta tipada confirmou Index Scan na PK, com filtro de provedor; o otimizador pode escolher varredura sequencial em tabelas pequenas |
| Eventos e observabilidade | Correlação/causação persistidas, snapshots imutáveis, ausência de eventos adicionais em replay e identificadores disponíveis nos logs de erro |
| Reconciliação | REPEATABLE READ, detecção de corrupção introduzida pelo teste, resposta/log/métrica de divergência e ausência de reparo silencioso |
| Fx e migrations | Validação do grafo sem infraestrutura, start/stop real, fechamento do listener, migrations sobre dados existentes e up/down/up em banco temporário |

A [matriz de requisitos](REQUIREMENTS.md) relaciona cada requisito à implementação e aos testes executáveis. O [roteiro manual](MANUAL.md) explica os resultados esperados das chamadas.

## Método e limites

A prova HTTP/SQS mantém a carteira bloqueada, aguarda o consumidor chegar ao banco, inicia o HTTP e percorre a cadeia de bloqueios para exigir ambas as entradas em espera. Isso separa o tempo de entrega do broker do prazo de processamento.

Os testes de inbox permitem uma janela de visibilidade de 30 segundos após uma resposta de long polling perdida no encerramento anterior. A espera pela DLQ permite até 90 segundos para cobrir essa janela e os backoffs das cinco entregas, mantendo a exigência da mensagem exata e de ausência de débito adicional. O timeout geral da suíte é de dez minutos.

No teste de publicação após crash, a deduplicação FIFO pode suprimir o segundo envio dentro da janela do broker. A prova combina nova tentativa registrada na outbox e identidade/conteúdo do evento recebido; não alega observar duas entregas nessa janela.

Estes resultados abrangem os cenários descritos; não são benchmark de capacidade nem certificação de produção. Não foi testada durabilidade de crash do próprio emulador, nem realizado deploy AWS real. A publicação não garante ordem estrita por carteira; consumidores devem tratar duplicatas e lacunas de walletVersion. As limitações e decisões estão em [ARCHITECTURE.md](../ARCHITECTURE.md).
