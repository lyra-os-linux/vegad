# Matriz de qualificação da preparação inicial

A preparação apenas importa chaves fixadas, atualiza metadados, publica as
contagens e registra conclusão. Os testes não executam upgrades no host.

## CI obrigatório

`go test ./...` inclui os cenários abaixo. A orquestração usa dependências por
invocação (`firstUpdateJob` e `preparationPersistence`), sem trocar funções
globais nem executar comandos administrativos nos testes de falha injetada.

| Contrato | Cobertura |
| --- | --- |
| Ordem, sucesso, contagens e marcador depois da publicação | `TestPrepareInitialRepositories`, `TestFirstUpdateJobFailureThenRetry` |
| Falha de detecção, importação, refresh, listagem, publicação, gravação inicial/final de estado e marcador | `TestFirstUpdateJobFailureThenRetry` |
| Chave desconhecida deixa proposta/estado pendente | `TestPreparationDiscoversReviewBeforeExiting`, `TestFirstUpdateJobFailureThenRetry`, testes de proposta em `internal/distro/preparation_key_test.go` |
| Nenhuma conclusão após falha e sucesso após nova tentativa usando os mesmos arquivos | `TestFirstUpdateJobFailureThenRetry` (nove cenários) |
| Publicação atômica do marcador e limpeza após falha | `TestCompletionMarkerCleansFailedAtomicPublication`, `TestWriteFirstUpdateMarkerCreatesParent` |
| Sessão live, marcador concluído e instalação legada não iniciam trabalho | `TestFirstUpdateJobEligibilitySkipsAllAdministrativeWork` |
| RPM novo, upgrade pendente/concluído/legado e marcadores existentes | `TestFirstUpdateRPMLifecycle` (scriptlets reais em raiz temporária) |
| Chaves distribuídas e importação idempotente | `TestShippedKeyringMatchesPinnedFingerprints`, `TestTrustedPackageKeysPrivateRPMDatabase` |
| Cancelamento em cada etapa e limpeza dos subprocessos | `TestPreparationCancellationNeverCompletesOrStartsNextStage`, `TestPreparationCommandsStopAndDrain` |
| Lock posterior à listagem e em ramo posterior de múltiplos erros | `TestUpdateAllPreservesLateRepositoryLock`, `TestExplainFirstUpdateLockFindsLaterJoinedCause` |
| Preparação sem snapshots e transações normais de melhor esforço | `TestInitialPreparationDoesNotAttemptSnapshots`, `TestSnapshotPolicyBestEffortAndHonestLogs` |

O job `system-contracts` instala GPG/RPM e executa a validação das chaves com
`VEGA_REQUIRE_KEYRING_TESTS=1`, `-count=1` e `-v`. A ausência dessas ferramentas
faz o job falhar, em vez de passar por skip. O teste
`TestRequiredKeyringToolsCannotSilentlySkip` verifica essa proteção. O job
`go` permanece portável para ambientes de desenvolvimento sem RPM/GPG.

## Qualificação controlada do serviço

As VMs usam systemd, D-Bus e Polkit reais, sem rede ou discos do host. As
instruções de compilação, `build`, `pack` e `run` estão em
[first-update.md](first-update.md). Elas complementam o CI e não são executadas
pelo job padrão: dependem do kernel e das ferramentas QEMU/openSUSE do host.

| Harness | Contrato |
| --- | --- |
| `scripts/check-preparation-vm.py` | Consulta sem privilégio, recusa/autorização, retomada de unidade inativa/falha, proteção contra reiniciar trabalho ativo, persistência após reinício |
| `scripts/check-preparation-key-vm.py` | Proposta produzida e consumida em processos diferentes, token antigo e URL alterada recusados, aprovação válida retoma preparação |
| `scripts/check-preparation-stop-vm.py` | Unidade empacotada, drenagem do comando, TERM/KILL de processo resistente e descendente, nova execução conclui sem limpeza manual |

Resultado esperado em cada log: `LYRA_PREPARATION_VM_RESULT=0`. Os comandos
RPM/Zypper dessas VMs são controlados; os testes com keyring/banco RPM temporário
exercitam as ferramentas reais separadamente. Não há alegação de validação de
uma queda física de energia ou de uma transação com repositórios de produção.
