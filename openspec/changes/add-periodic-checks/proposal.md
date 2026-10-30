# add-periodic-checks: parte do vegad

Proposta-mãe:
[lyra-ecosystem/openspec/changes/add-periodic-checks](https://github.com/lyra-os-linux/lyra-ecosystem/tree/main/openspec/changes/add-periodic-checks),
aprovada em 29/10/2026 (frequência semanal).

- `vegad-health-check.timer` (semanal, `Persistent`, atraso aleatório de 1 h) e
  o serviço que roda `vegad check-health`, que só pede ao daemon um `Start`
  com a opção `periodic` (aceita só do root). Desktop e Server.
- O job periódico só aceita `disk-health`, `logs`, `platform` e `security`;
  roda cada ferramenta com `nice -n 19 ionice -c3`; é adiado em silêncio com a
  bateria abaixo de 30% sem carregador ou com outra operação em andamento.
- A comparação (`comparePeriodic`) guarda em
  `/var/lib/vega/diagnostics/periodic.json` quando cada achado (código e
  argumentos do conserto) apareceu, o desgaste do SSD por disco (60 dias) e se
  as correções atrasadas já foram avisadas.
- `GetPeriodic`/`SetPeriodic` usam o `systemctl` e um drop-in em
  `/etc/systemd/system/vegad-health-check.timer.d/` para a frequência diária.
- O `%post` liga o timer uma vez só (marcador
  `/var/lib/vega/health-check.enabled-once`), para que quem desligou no Vega
  continue com ele desligado depois das atualizações.
