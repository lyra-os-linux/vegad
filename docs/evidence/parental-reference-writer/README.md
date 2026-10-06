# Writer root, cortes de processo e retomada exata

`python3 docs/evidence/parental-reference-writer/verify.py` verifica recibos/hashes
sem iniciar VM. Go1.22.12 -buildvcs=false, binário gzip/hash verificado.

VM em overlay novo sobre a imagem de laboratório já preparada na etapa Snapper;
três backingfiles (Snapper qcow2, Btrfs qcow2, raw original) são preservados. Segundo
qcow2 novo/vazio512MiB com serial lyra-ref-domain-test. ActualQEMUargv/root/kernel
marker/serial/size/nomount/wipefsblank são guardas anteriores ao mkfs exclusivo em
vdb guest. Nenhum disco/mount/pacote/fstab/subvolume do host é alterado.

Factory writer/source root, qualifier Btrfs real, root+três autoridades diferentes
montadas, uma por cenário. Referência inicial, emissor completo e maintenance são
fixtures explícitas. Cada filho termina os.Exit73 após journal durable, reference
durable ou pin durable. Parent observa status73, readerRecoveryRequired, resume
nilDenied e wrong-opConflict; depois resume exato completo, source novo, lockinode
original e somente três arquivos finais. Catálogo permanece byteidêntico.

Go confirma namespace/guards antes de mutações. guest.py desmonta root e todas
as autoridades/vdb; pointdir removido, contas/PAM/DBus/Polkit/Snapper/SELinux antes e
depois iguais. Fixtures permanecem somente no disco OFF. QEMUexit0 e backingchain
hash/size/inode/mtime iguais. SELinuxPermissive não qualifica MAC.

Tentativa inicial: teste Go passou, mas guest.py ainda exigia um campo legado de
snapshot e o harness falhou. Config/catálogo/contas intactos, limpeza/desligamento
normal/backings preservados. Recibos preservados em analysis/2026-10-06/
vegad-parental-reference-writer/attempt-1, sem rotular tentativa como passe.
Driver corrigido; execução aprovada usa OUTROS overlays/discos novos.

Não qualifica primeira emissão/provisionamento, autoridade completa real/Polkit,
perda física de energia/kernel crash, reboot/GRUB/ISO, admissão ou controle parental
funcional. Apenas avanço de store existente e retomada da mesma operação. Alteração
intencional de reference_linux.go adiciona recusa de pending; manifests de etapas
anteriores são históricos e não afirmam os hashes do checkout atual.
