# add-space-cleanup: parte do vegad

Proposta-mãe, requisitos e design:
[lyra-ecosystem/openspec/changes/add-space-cleanup](https://github.com/lyra-os-linux/lyra-ecosystem/tree/main/openspec/changes/add-space-cleanup),
validada pelo mantenedor em 29/10/2026.

Este repositório entrega a interface `org.lyraos.Vega1.Cleanup`
(`space-cleanup-v1`) e o achado `storage.low-space` no teste `platform`:

- `internal/cleanup`: medição e limpeza das seis categorias do sistema;
- `CleanupService`: `Scan` como leitura silenciosa
  (`org.lyraos.vega.storage.read-admin`) e `Clean` como job sob o coordenador
  de exclusividade (`org.lyraos.vega.storage.cleanup`).

Escolhas de implementação, dentro dos requisitos:

- **Journal:** em vez de `journalctl --vacuum-size=500M`, que não garante o
  boot anterior, o vegad apaga só os arquivos arquivados que comprovadamente
  terminam antes do boot anterior (o arquivo seguinte da mesma cadeia começa
  antes dele), do mais antigo para o mais novo, até o journal caber em 500 MB.
- **Cache de pacotes:** `zypper clean` sem `--all`. Ele apaga só os pacotes
  baixados, que é o que foi medido; os metadados ficam, e a próxima
  atualização não baixa tudo de novo.
- **Kernels:** além da regra `multiversion.kernels`, o vegad recusa
  (`reason=protected`) um plano que tire o kernel em uso ou o mais novo. O
  espaço volta ao disco quando os snapshots que ainda guardam os arquivos
  forem limpos.
- **Snapshots:** o número é um mínimo (`minimum`), e o tamanho é desconhecido
  sem as quotas do btrfs.
