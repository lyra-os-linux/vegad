# Admissão Flatpak: credenciais do socket — 07/10/2026

Branch `feat/parental-flatpak-admission`, baseada em `d33ce837` do vegad. Novo módulo
experimental `internal/parental/flatpak_admission_linux.go`: aceita somente
um frame JSON curto com `app_id` canônico, recusa chaves extras/duplicadas,
argumentos, ref, caminho e alegações de UID, e lê UID/GID/PID via
`SO_PEERCRED` e contexto SELinux via `SO_PEERSEC`. Rótulo ausente/`kernel`
falha fechado. Não consulta política, não resolve ref/deployment e **não lança
processos**. O contexto retornado ainda precisa de comparação exata com o
registro parental ativo; a observação do socket sozinha não autoriza nada.

Testes do pacote `internal/parental` passaram com Go 1.22.12 isolado. A VM
descartável Leap 16.1 compilou a suíte no host e a executou em SELinux
Enforcing: pedido válido vindo do UID 1003 produziu contexto
`system_u:system_r:unconfined_service_t:s0`; mensagem que alegava `uid:0`
foi recusada. Este é um **contexto comum de fixture**, não o domínio da conta
supervisionada. A próxima prova deve partir de uma sessão realmente mapeada
ao domínio `lyra_test_child_a_t`, validar UID/GID/geração contra o catálogo e
mostrar que uma conexão passada a outro processo não vira autorização.

O runner restaurou SELinux Permissive, QEMU saiu com código 0 e a VM está OFF.
`qemu-img check` não encontrou erros; o backing permaneceu SHA256
`35a72ca497992d6cc46ddfd0b59396cbeb11bb9d3efafbd9d84b31e9d5f00435`.
`result.json`, os logs da VM e `verify.py` neste diretório preservam o teste. Sem instalação no
host, OBS, ISO ou integração de produto. Issue #102 segue aberta/NO-GO.

## Continuação: dois domínios supervisionados em Enforcing

Novo overlay descartável `/tmp/lyra-flatpak-supervised-peer-20261007/system.qcow2`
usou a base com duas contas e domínios SELinux separados. Os clientes de teste
foram executados como `parentaltest` (UID 1003, `lyra_test_child_a_t`) e
`parentaltest2` (UID 1011, `lyra_test_child_b_t`). Em quatro casos, o serviço
leu UID/GID/PID e contexto do kernel. O pedido válido de cada conta passou;
o payload que declarava `uid:0` foi recusado em ambas. O teste Go correu com
SELinux Enforcing, retornou PASS e registrou `product_integrated=false`.

Esta prova usa `runcon` e clientes C em uma fixture. Não autentica o GDM,
não vincula o peer ao catálogo parental, não resolve nem inicia Flatpak e não
testa transferência de socket. Portanto, ainda não qualifica #102. Os arquivos
`supervised-result.json`, `lyra-flatpak-supervised-peer-20261007-commands.jsonl`,
`supervised-peer.cil`, `supervised-peer-client.c`, os runners e o verificador
permitem revisar o ensaio. O runner restaurou Permissive, a VM desligou com
QEMU exit 0, `qemu-img check` passou e a base permaneceu com o mesmo SHA256
acima. Próximo: validar vínculo exato entre UID/GID/domínio e a geração ativa
do catálogo, incluindo socket passado entre processos; depois qualificar
ref/deployment e execução permitida/negada.
