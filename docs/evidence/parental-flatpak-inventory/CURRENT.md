# Inventário Flatpak de dois apps locais — 07/10/2026

Overlay descartável `/tmp/lyra-flatpak-inventory-20261007/system.qcow2`
baseado no ensaio de inode compartilhado, com `org.lyra.TestApp` e
`org.lyra.TestAppB` instalados. `probe.py` fez somente leituras, com marcador
de VM e serial do disco verificados. `result.json` preserva ref, commit,
remote, runtime, localização, link `active`, forma do arquivo `deploy` e
configuração GPG dos remotes. O verificador passou.

Os apps têm refs e commits completos distintos, embora compartilhem inode de
payload na prova anterior. Seus links `active` apontam aos respectivos commits.
Ambos usam o mesmo runtime, cujo commit completo foi observado. Os dois
remotes locais de fixture têm `gpg-verify=false` e
`gpg-verify-summary=false`. Portanto, **não existe identidade de publicador
verificada nesta fixture**; a aprovação deve ficar presa ao commit exato.

`flatpak info` fornece os campos em chamadas separadas, e o arquivo `deploy`
não é texto INI. Este ensaio não fornece um snapshot atômico ou um FD que fixe
deployment/runtime até a criação do processo, nem atesta assinatura. Não usar
este `probe.py` como fonte de autorização. Próximo trabalho: inventário
confiável com identidade de assinatura e fixação da versão ativa durante o
lançamento; quando isso estiver pronto, integrar ao seletor puro e testar
troca de versão entre admissão e execução.

QEMU encerrou com código 0 e a VM está OFF; `qemu-img check` não encontrou
erros. Backing SHA256 antes/depois:
`6293b6a9ae0e8304ed9e7ffc24705252b4cd72917d0dedb2f48542d7eb4cc5e7`.
Nenhum pacote do host, OBS ou ISO foi alterado. #102 OPEN/NO-GO.
