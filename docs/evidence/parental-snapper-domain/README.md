# Snapper real e referência vinculada à configuração

Verificação sem iniciar VM: `python3 docs/evidence/parental-snapper-domain/verify.py`.

Go 1.22.12, -buildvcs=false, binário gzip transferido com hash conferido.
Guest Snapper 0.12.1, libsnapper 7.4.3, RPMs oficiais Leap 16.1 instalados antes do
baseline. `prerequisites.json` registra instalação e rede de laboratório; `run.py`
registra a execução. vm.py/host.py/guest.py e hashes estão arquivados. Caminhos são
fixos deste laboratório, não um instalador ou receita portátil.

Overlay de sistema novo, backing qcow2 da etapa Btrfs (OFF) e backing raw original;
ambos conferidos antes/depois. Segundo qcow2 vazio512MiB, serial lyra-ref-domain-test,
actualQEMUargs/marker/serial/size/mount/wipefs guard antes de formatar SOMENTE vdb.
Nenhum disco/mount do host é alterado. Root e authority são subvolumes bind mounted
no filesystem do disco descartável. A configuração é explicitamente provisionada
como fixture root em /etc/snapper/configs/lyra-parental-lab, sem registrar timers;
.snapshots é subvolume privado. ALLOW_USERS/GROUPS vazios, SYNC_ACL=no, timeline=no.

O CLI Snapper --no-dbus create cria o snapshot; o conteúdo do catálogo é conferido.
Restauração offline via Btrfs e remount da árvore do catálogo preserva o pin/ref
atuais e exige BlockedRestoreRequired. Delegação de Snapper a usuário recusa;
mudança de comentário altera o witness. Testes da topologia/readonly/stale ref/pin
continuam presentes. Configuração fixture removida, vdb desmontado, mountdir ausente,
contas/PAM/DBus/Polkit/Snapper/SELinux iguais durante ensaio; QEMU exit0/VM OFF,
ambos backingfiles idênticos em hash/size/inode/mtime. Fixtures só no disco OFF.

A instalação guest de dependências e rede ocorre ANTES do baseline; não alegar
overlay sem preparação. SELinux é Permissive; cap/NSS/emissão são fixtures.
Não executa snapper rollback ou certifica reboot/default-subvolume/GRUB/ISO,
registro SNAPPER_CONFIGS, guardian, publicação de recuperação, monotonicidade global
ou proteção a restore da autoridade junto ao catálogo. Não ativa parental no host.
