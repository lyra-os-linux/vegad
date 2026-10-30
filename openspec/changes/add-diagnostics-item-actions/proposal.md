# add-diagnostics-item-actions: parte do vegad

Proposta-mãe:
[lyra-ecosystem/openspec/changes/add-diagnostics-item-actions](https://github.com/lyra-os-linux/lyra-ecosystem/tree/main/openspec/changes/add-diagnostics-item-actions),
aprovada pelo mantenedor em 30/10/2026.

- `Diagnostics.Remedies`: leitura pública intermediada, só ids e índices.
- `Diagnostics.RepairFindings`: o mesmo job do `Repair`, com os argumentos só
  dos achados escolhidos.
- `Diagnostics.MarkLogsSeen`: o corte fica em
  `/var/lib/vega/diagnostics/logs-seen.json` e nunca volta para trás. O teste
  `logs` grava `examinedUntil` (o momento em que começou a ler o journal), que
  vira o corte; relatórios antigos, sem essa métrica, recebem `NotSupported`.
- O teste `logs` aplica o corte com `--since=@<segundos>` (arredondado para
  cima) e compara o `StateChangeTimestamp` dos serviços com falha.
