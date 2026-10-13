# Sondagem preliminar do layout parental — 06/10/2026

Esta evidência é parcial e **não qualifica** o instalador nem o rollback de
raiz. Uma VM Leap 16.1 descartável iniciou sobre uma camada qcow2 nova; o disco
de base permaneceu inalterado. O segundo disco qcow2, novo e vazio, foi
formatado como Btrfs somente depois de conferir o marcador da VM, seriais
`lyra-admission-test`/`lyra-ref-domain-test`, tamanho e ausência de mounts.

No disco Btrfs descartável foram criadas as subvolumes irmãs
`/@lyra-parental` e `/@lyra-parental-authority`. Snapper 0.12.1 criou a
configuração `lyra-parental`, registrou-a em `SNAPPER_CONFIGS`, criou
`.snapshots` e aceitou o snapshot inicial somente leitura. Depois de gravar as
três linhas de mount no `fstab` e reiniciar o sistema, scope, authority e o
subvolume aninhado `.snapshots` foram montados nos destinos esperados;
`snapper list` continuou funcionando.

O guest usa raiz ext4 em `/dev/vda` e o Btrfs parental foi posto em `/dev/vdb`.
Isso comprova apenas o comportamento Snapper/fstab observado em Leap 16.1. Não
executa o instalador Rust, não testa subvolumes parentais no mesmo filesystem
Btrfs da raiz Lyra, nem rollback de `/@`, atualização, reinstalação ou ISO.

Foi executado `snapper --no-dbus -c lyra-parental rollback 1`; Snapper recusou
com `Command 'rollback' cannot be used on a non-root subvolume
/var/lib/lyra/parental.` Portanto, recuperação do catálogo precisa ser
qualificada como restauração dos dados de snapshot e detecção posterior do
witness/pin pelo vegad. Rollback do sistema é um caso separado e ainda
pendente.

QEMU encerrou via `systemctl poweroff` (exit 0). Os SHA-256 dos dois discos de
base foram iguais antes e depois: system qcow2
`334e5bb66bbcdebd3145d8b65172719e374925b53e45df0bf6735725269241dd`; domain
qcow2 `0c8d7ad7838862e6b19a28aba210dc1db083fa62c161ff7d9315afdfde0dbf8a`.
Todos os writes do guest ficaram na camada de sistema nova e no segundo disco
descartável. Os comandos, argv QEMU e console serial estão arquivados ao lado.
