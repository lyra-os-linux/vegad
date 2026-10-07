# Argumentos fechados de lançamento Flatpak — 07/10/2026

`BuildFlatpakRunArgs` recebe somente o deployment selecionado pelo servidor e
produz os parâmetros `--system`/`--user`/`--installation`, arquitetura,
branch, runtime, versão do runtime e os commits completos de app/runtime.
Não aceita argumentos, caminhos ou FDs fornecidos pelo cliente. Testes Go
verificam a lista exata e recusam identidade/escopo/commit malformados.

Overlay descartável `/tmp/lyra-flatpak-run-args-20261007/system.qcow2` com os
dois apps de fixture. O teste Go opt-in compôs os argumentos e chamou o
Flatpak 1.16.6 da VM como `parentaltest` (UID 1003). A e B lançaram com seus
pares corretos; o commit de B pedido como A e um runtime inexistente foram
recusados. Os quatro casos passaram. `result.json`, logs, runner e `verify.py`
preservam a prova.

O teste rodou com SELinux Permissive e inventário de fixture. **Não** prova
enforcement, origem/publicador autenticados, snapshot atômico, revogação,
conteúdo de helpers nem bloqueio do `flatpak run` direto. A observação anterior
mostrou que `--app-fd` pode montar B ao pedir A; por isso o domínio da criança
precisa ter todas as rotas diretas bloqueadas e o broker precisa construir
sozinho qualquer FD. A função ainda não é um broker nem executa processo.

QEMU saiu com código 0, VM OFF, `qemu-img check` PASS. Backing SHA256
`6293b6a9ae0e8304ed9e7ffc24705252b4cd72917d0dedb2f48542d7eb4cc5e7`
inalterado. Sem host/OBS/ISO alterado. #102 OPEN/NO-GO.
