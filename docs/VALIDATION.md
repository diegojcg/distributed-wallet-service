# Validação — 27/09/2026

Ambiente: Linux amd64, Go 1.27.1, Docker Engine 25.0.2 e Compose 2.19.1. Serviços reais: PostgreSQL 17.6, Keycloak 26.7.4 e MiniStack 1.5.17 com AUTH=true.

## Comandos e evidências

| Comando/verificação | Resultado |
| --- | --- |
| go test ./... | Passou |
| go test -race ./... | Passou |
| go vet ./... | Passou |
| go mod verify | Todos os módulos verificados |
| gofmt -l cmd internal migrations tests | Nenhum arquivo sem formatação |
| GOTOOLCHAIN=go1.27.1 go run golang.org/x/vuln/cmd/govulncheck@latest ./... | No vulnerabilities found |
| scripts/clean-check.sh | Passou: integração em 233,771 s, Fx em 1,064 s; imagem final e três réplicas saudáveis |
| docker compose up --build --scale app=3 -d --wait | Três réplicas saudáveis no ambiente principal |
| GET /health/ready em cada réplica final | HTTP 200, status ready |
| Leitura dos arquivos IAM dentro de cada réplica | Worker legível; provider-a/provider-b/observer inacessíveis ao UID da aplicação |
| Comparação docs/CHALLENGE.md com README original do Git | Idênticos |
| docker compose config --quiet e sh -n scripts/*.sh | Passaram |

A verificação limpa cria uma cópia de fontes, outro projeto Compose e volumes vazios, sem reutilizar o banco ou a identidade do ambiente principal. Provisiona filas, políticas/usuários IAM, realm e clients do Keycloak e as migrations. Executa testes unitários/race, vet, módulos e integração; depois constrói a imagem final e inicia três réplicas. Ao terminar, remove somente os containers e volumes desse ambiente descartável.

As permissões dos arquivos IAM foram endurecidas também no ambiente principal e verificadas dentro das três réplicas após o build final. Segredos gerados ficam em work/ e em volume local, fora do Git e do pacote de fontes.

## Cenários confirmados

- Tokens reais do Keycloak: ausência, inválido, expirado, audience incorreta, roles e isolamento entre provedores; acesso negado sem efeito financeiro.
- Precisão/overflow monetário, política de zero, OPENING interno e rejeição de OPENING externo por HTTP/SQS.
- Três processos com pools e memória separados: 50 duplicatas, disputa 80/80 sobre saldo 100, replay histórico e oito carteiras com 80 operações concorrentes.
- REFUND/ROLLBACK, bloqueio de segunda reversão, LOSS sem mudança de saldo/versão, pendência resolvida e rejeição por expiração/referência mal-sucedida.
- Ledger imutável, identidade/versão de carteira protegidas, unicidades, transação terminal imutável, saldo e ledger reconciliados.
- Falha SQL permanente provocada após a atualização da transação: rollback financeiro, auditoria FAILED e inbox atômica, replay e redrive para DLQ.
- Dez entregas SQS efetivas da mesma operação já enviada por HTTP, inbox conflitante, falsificação do provedor e DLQ sem débito extra.
- Correlação HTTP/SQS e causação nos eventos, ausência de eventos adicionais em replay e snapshots imutáveis.
- Reinício de todas as instâncias preservando idempotência e pendências. Queda real do PostgreSQL retorna erro transitório; queda do broker preserva os eventos confirmados e sua publicação posterior.
- Encerramento real de processo após commit HTTP, após commit SQS/antes do ACK e após publicação/antes da confirmação da outbox; retomada por outra instância com o mesmo registro/eventId.
- Reconciliação em snapshot consistente, detecção de corrupção introduzida pelo teste administrativo, resposta/log/métrica de divergência e ausência de reparo silencioso.
- Start/stop Fx com infraestrutura real e fechamento do listener. Migrations up/down/up em banco temporário, removido ao final.

Os testes de integração compilam os três processos com `-race`. Os failpoints só existem nas builds de teste; um arquivo marcador comprova que cada interrupção realmente aconteceu. PostgreSQL, Keycloak e SQS não são substituídos por mocks.

## Ajustes observados na execução

A primeira execução limpa revelou uma espera insuficiente no teste de inbox: 15 segundos não cobriam uma resposta de long polling perdida durante o encerramento anterior, seguida do visibility timeout de 30 segundos. O limite passou para 45 segundos. A verificação continua exigindo dez registros de inbox e um único débito; não foi substituída pela deduplicação FIFO. A suíte completa tem timeout de dez minutos.

A migração dos metadados de correlação também precisou drenar triggers diferidos antes de outro ALTER TABLE. O cenário de migração sobre dados existentes foi reaplicado com sucesso, além do up/down/up em banco vazio.

## Alcance

A matriz em REQUIREMENTS.md identifica implementação e testes por requisito. Estes resultados demonstram os cenários exercitados; não são benchmark de capacidade nem certificação de produção. O emulador preserva estado no encerramento normal, sem promessa de durabilidade equivalente à AWS diante de crash do próprio broker. As limitações e os diferenciais opcionais não implementados estão em ARCHITECTURE.md.

Logs detalhados ficam em work/, ignorados pelo Git. Nenhum repositório remoto foi publicado e nenhum e-mail foi enviado nesta execução.

## Auditoria posterior

Uma nova rodada completa e regressões adicionais foram executadas em 27/09/2026. Veja AUDIT.md para os resultados, as duas correções pontuais de HTTP, as provas adicionais de SQL/outbox e o smoke das imagens finais. MANUAL.md contém o roteiro para validação humana.

As correções posteriores da revisão independente e sua nova execução completa estão em REVIEW-FIXES.md.
