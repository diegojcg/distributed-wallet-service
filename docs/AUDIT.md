# Auditoria adicional por camada — 27/09/2026

Objetivo: confirmar as garantias existentes com provas adicionais e um smoke das imagens, mantendo arquitetura e infraestrutura escolhidas.

## Correções pontuais

1. **UUIDs nas rotas:** a biblioteca aceitava URNs e outros formatos equivalentes, mas a string original seguia para o banco. Uma URN provocava 503 em vez de consulta válida/não encontrado. Agora os IDs são normalizados antes das consultas; UUID nulo/inválido retorna 400. Abrange carteira, ledger, reconciliação e consulta de transação por ID. Testes: TestCanonicalRouteID e TestHTTPBoundaryAudit/CanonicalRouteIDs.
2. **Entrada incompleta:** um objeto sem campos obrigatórios podia cair na comparação de providerId e receber 403. O domínio agora valida o corpo antes dessa comparação; erro de entrada retorna 400 sem acesso ao caso de uso. Um corpo válido com providerId diferente do token continua recebendo 403. Teste: MissingBusinessFieldsAreInvalidInput.

## Evidências adicionais

| Camada/decisão | Verificação |
| --- | --- |
| Money | FuzzMoneyRoundTrip por 20 segundos: 10.229.670 execuções do fuzzer, sem falha; não é benchmark de capacidade do serviço |
| HTTP | UUIDs equivalentes/nulos, duas chaves que apontariam para transações distintas, normalização monetária, limites de paginação, cursor de outra carteira e percurso completo sem repetição |
| Privilégios PostgreSQL | Login runtime recebe 42501 ao tentar editar/excluir/truncar ledger ou desabilitar proteções; administrador recebe 23514 pelos triggers de imutabilidade |
| Integridade SQL | Saldo negativo, saldo sem ledger, payload da outbox alterado e ledger ligado à carteira errada são recusados |
| Atomicidade | Erro de serialização provocado no INSERT da outbox desfaz inbox, operação, alteração de saldo e ledger; ausência de movimentação parcial |
| Concorrência da outbox | Outro claimant não obtém eventos com lease ativo; lease expirado é retomado com novo token e mesmos ID/payload |
| Worker obsoleto | Confirmar ou reagendar com o token antigo não altera o lease novo; somente o claimant atual confirma |
| Imagem final | Smoke autenticado nas três réplicas: cinco tipos externos, autorização, replay histórico, paginação e reconciliação; saldo final 85.00 e cinco lançamentos |

Os testes SQL adicionais usam outro banco temporário, removido ao final, com conexões de administrador e runtime distintas. A integração completa usa outro projeto Compose, outras portas e volumes vazios. Uma execução externa de integração foi observada no ambiente principal; nenhum processo dela foi interrompido por esta auditoria.

## Resultado de execução

- `scripts/clean-check.sh`: passou. Integração completa com race detector: 292,979 s; ciclo de vida Fx: 1,087 s. Incluiu os novos testes iniciais de fronteiras/SQL, os testes existentes de concorrência e os três crashes reais.
- Após os últimos ajustes de validação e ampliação da prova de inbox/permissões: `scripts/clean-check.sh -run 'TestHTTPBoundaryAudit|TestStorageAudit'` passou em outra cópia e volumes novos; integração selecionada em 5,931 s, além de testes unitários/race, vet, módulos, build e smoke das imagens.
- `go test -race ./...`, `go vet ./...`, formatação, sintaxe dos scripts e configuração Compose: passaram.
- `govulncheck`: nenhuma vulnerabilidade conhecida encontrada na execução final.
- Smoke final do ambiente principal: três réplicas, saldo 85.00, cinco lançamentos e reconciliação consistente. As réplicas ficaram saudáveis para validação manual.

Os logs locais desta rodada ficam em `work/audit/`; não entram na entrega. A evidência anterior permanece em VALIDATION.md, e a matriz de requisitos foi ampliada em REQUIREMENTS.md.

## Limites da conclusão

As provas abrangem os contratos e falhas descritos; não permitem prometer ausência absoluta de defeitos. Não foram feitos benchmark de capacidade, deploy AWS real ou teste de durabilidade de crash do próprio emulador. A ordem de publicação de eventos por carteira continua sem garantia estrita; consumidores devem tratar duplicatas e lacunas de walletVersion conforme EVENTS.md.
