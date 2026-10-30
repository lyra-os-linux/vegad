# add-cli-diagnostics: parte do vegad

Proposta-mãe: [lyra-ecosystem/openspec/changes/add-cli-diagnostics](https://github.com/lyra-os-linux/lyra-ecosystem/tree/main/openspec/changes/add-cli-diagnostics), aprovada em 30/10/2026.

A regra `50-vegad-remote-admin` libera em silêncio, para membros de `wheel` em sessão remota, `hardware.read-admin`, `storage.read-admin` e `network.diagnose`, as mesmas leituras que a sessão local ativa já faz sem senha. O `check-packaging-contracts.sh` exige que cada uma delas seja `allow_active yes` na política antes de a regra remota liberar.
