# add-boot-analysis: parte do vegad

Proposta-mãe:
[lyra-ecosystem/openspec/changes/add-boot-analysis](https://github.com/lyra-os-linux/lyra-ecosystem/tree/main/openspec/changes/add-boot-analysis),
aprovada em 29/10/2026.

- Teste `boot` (`internal/diagnostics/boot.go`): fases do
  `systemd-analyze time`, os cinco serviços mais lentos do `blame`, a cadeia
  crítica até `graphical.target` e os discos que esgotaram o tempo no journal.
- Remédios `disable-wait-online` e `disable-service`, com snapshot, que
  conferem tudo de novo na hora (o relatório pode ser antigo) e dizem no
  resumo como desfazer (`systemctl enable`).

Escolhas de implementação, dentro dos requisitos:

- **Só o que atrasou o boot:** o `blame` lista também serviços de timer que
  rodam depois do login (na workstation, o `vegad-update-check.service`, com
  1min 59s). Um serviço só conta se começou antes de `graphical.target`
  (`InactiveExitTimestampMonotonic` ≤ `ActiveEnterTimestampMonotonic`).
- **Serviço do usuário:** `UnitFileState` `enabled` com `UnitFilePreset`
  `disabled`. O que o preset da imagem habilita só recebe orientação.
- **`network-online.target` necessário:** o `disable-wait-online` não é
  oferecido se outra unidade está em `WantedBy`/`RequiredBy` do
  `network-online.target` (na workstation, o `iscsi.service`).
- **Resumo:** o tempo até o login (total menos o userspace mais o tempo até
  `graphical.target`).
