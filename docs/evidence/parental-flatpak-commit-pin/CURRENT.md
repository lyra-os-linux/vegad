# Commit explícito e FD de app no Flatpak — 07/10/2026

VM descartável `/tmp/lyra-flatpak-commit-pin-20261007/system.qcow2`, sobre
o overlay com dois apps que compartilham inode de payload. Os scripts
`probe.py`, `fd-probe.py` e `fd-child.py` são restritos por marcador de VM e
serial de disco. O teste rodou como `parentaltest` (UID 1003), com SELinux
Permissive para isolar a semântica do launcher. **Não prova enforcement.**

Com `flatpak run --commit=<app> --runtime-commit=<runtime>`, A e B iniciaram
com seus respectivos commits. Pedir A com o commit de B foi recusado como
"não instalado"; pedir runtime de commit inexistente também foi recusado.
O RPM Flatpak 1.16.6 da VM aceita esses parâmetros e os conecta à escolha do
deployment. Isso é uma opção para fixar um par conhecido **desde que** o
broker impeça o cliente de passar argumentos e valide a origem do par.

`--app-fd` não vincula o conteúdo ao app ID. O ensaio criou temporariamente
`b-only` somente no deployment B. Ao pedir `org.lyra.TestApp` com FD de A,
`/app/bin/b-only` não existia e falhou; o mesmo pedido com FD de B executou e
retornou 0. O arquivo de marcação foi removido antes do desligamento. O FD
deve ser obtido pelo broker de uma origem confiável e associado ao ref/commit
aprovado; deixar a criança chamar `flatpak run --app-fd` diretamente é evasão.

Este experimento não testa troca de `active` entre verificação e execução,
atestado de assinatura, contenção MAC, helpers, revogação ou conta comum. O
arquivo `deploy` pode ser GVariant e chamadas `flatpak info` separadas não
formam snapshot atômico. O próximo gate é construir a fonte de inventário e
associá-la a `--commit`/`--runtime-commit` sob bloqueio de execução direto.

`commit-result.json`, `fd-result.json`, logs e `verify.py` preservam a prova.
QEMU saiu 0 e a VM está OFF; `qemu-img check` passou. Backing SHA256 antes/
depois `6293b6a9ae0e8304ed9e7ffc24705252b4cd72917d0dedb2f48542d7eb4cc5e7`.
Sem alteração em host, OBS ou ISO. #102 OPEN/NO-GO.
