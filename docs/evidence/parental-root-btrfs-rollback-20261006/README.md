# Rollback de raiz Btrfs com scope parental irmão — 06/10/2026

A VM Leap 16.1 usou uma imagem qcow2 nova de 12 GiB, serial
`lyra-parental-root-t`, inicializada em Btrfs. O sistema de ensaio foi copiado
para uma subvolume `@`; no mesmo filesystem foram criadas `@lyra-parental` e
`@lyra-parental-authority`, com `.snapshots` próprios. O `fstab` montou raiz,
scope, authority e os dois `.snapshots`. Após o boot, as cinco montagens estavam
ativas sobre `/dev/vda`, ambos os configs (`root` e `lyra-parental`) listavam
snapshots e `SNAPPER_CONFIGS` continha os dois nomes.

Foi criado um marcador na raiz depois do snapshot inicial e executado
`snapper --no-dbus -c root --ambit classic rollback 1`. Após repetição para
confirmar a preservação de dados, o Snapper criou backup #6 e a cópia gravável
#7, e alterou o subvolume Btrfs padrão para `@/.snapshots/7/snapshot`. Depois
do reboot, `/` montou esse snapshot; o marcador posterior ao snapshot inicial
não existia. O testemunho no catálogo parental manteve hash
`a73f1e8432fb272437f27c9287b7cb52c64c68795b921387c440ca691044c186` e inode
261. Um sentinela adicional na authority manteve hash
`485812be98475291a393e9e6e5bf989dfad4e656500f4822987a82849951e02d` e inode
258. Scope, authority e seus `.snapshots` continuaram montados no filesystem
original.

Uma tentativa sem `--ambit classic` foi recusada por Snapper porque não inferiu
o modo. A repetição explícita foi bem-sucedida; o teste corresponde ao layout
clássico `/@` e ao comportamento usado pelo smoke test do Lyra.

Esta sondagem qualifica o comportamento Btrfs/Snapper do mecanismo, não a
sequência Rust do instalador. O disco Btrfs e os arquivos fstab/config foram
montados manualmente em VM, e a raiz de teste veio de cópia de outro guest
descartável. Não qualifica execução do instalador, ISO, upgrade ou reinstalação.
O gate `installedParentalLayoutReady()` continua fechado até esses casos serem
qualificados sobre a saída real do instalador.

O kernel foi `6.12.0-160100.4-default`. Um initrd exclusivo de ensaio carregou o
módulo Btrfs e suas dependências; sua receita, hashes e boot args estão
registrados nos arquivos vizinhos. O initrd distribuído/original não foi
alterado. As duas imagens de base permaneceram idênticas antes/depois:
`/tmp/lyra-parental-provision-20261006/system.qcow2` SHA-256
`334e5bb66bbcdebd3145d8b65172719e374925b53e45df0bf6735725269241dd` e
`/tmp/lyra-parental-provision-20261006/domain.qcow2` SHA-256
`0c8d7ad7838862e6b19a28aba210dc1db083fa62c161ff7d9315afdfde0dbf8a`. Todo o
Btrfs gravável do ensaio ficou em `root-btrfs.qcow2`, uma imagem descartável em
`/tmp/lyra-parental-layout-20261006`. QEMU terminou após `systemctl poweroff`.
