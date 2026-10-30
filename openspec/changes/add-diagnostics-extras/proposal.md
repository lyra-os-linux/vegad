# add-diagnostics-extras: parte do vegad

Proposta-mãe:
[lyra-ecosystem/openspec/changes/add-diagnostics-extras](https://github.com/lyra-os-linux/lyra-ecosystem/tree/main/openspec/changes/add-diagnostics-extras),
aprovada em 29/10/2026.

- O teste `platform` produz `backup.stale` (atenção) quando o backup mais novo
  de todas as configurações tem mais de 14 dias, ou nunca rodou, e
  `backup.none` (informação) sem configuração. Um backup recente em qualquer
  configuração basta.
- A idade vem do snapshot mais novo do restic (`resticSnapshotsContext`, 10 s
  por configuração). Se o destino não pode ser listado (disco desconectado),
  vale o último sucesso que o vegad passou a gravar em
  `<estado do backup>/last-success/<id>` ao fim de cada `restic backup`, e o
  texto diz que o disco não está conectado.
- O `diagnostics` não importa o `dbusserver`: a idade chega pelo
  `Env.Backups`, preenchido pelo serviço.
