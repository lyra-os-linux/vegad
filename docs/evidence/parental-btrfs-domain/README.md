# Ensaio Btrfs em disco descartável

Verificação: `python3 docs/evidence/parental-btrfs-domain/verify.py`.
A verificação confere recibos e hashes; não executa o laboratório.

O teste Go foi compilado com Go 1.22.12, `-buildvcs=false`. O binário fica somente
em analysis, e o hash enviado é registrado. `host-run.py`/`vm.py`/`host.py` são
arquivados junto ao driver `guest.py`; os caminhos fixos descrevem este ensaio,
não são um instalador nem uma receita portátil de produção.

A VM tem backing raw preservado e dois discos: overlay de sistema novo e qcow2
novo de 512MiB para Btrfs. O host confirma os argumentos reais de QEMU; o guest
exige root, marcador de kernel, serial/tamanho do segundo disco, ausência de mounts
e assinaturas antes de formatar exclusivamente `/dev/vdb`. O teste Go confirma
novamente serial e mount, sem formatar nem montar discos.

A referência inicial e a atual são fixtures explícitas de emissão root. Um
snapshot readonly real da árvore do catálogo é criado, a referência e o catálogo
avançam, e a árvore é substituída por um snapshot gravável da versão anterior.
O catálogo volta, enquanto referência/pin no subvolume irmão permanecem atuais.
O leitor root e o inspector diagnosticam recuperação bloqueada. A topologia real
recusa mesmo subvolume, catálogo em árvore aninhada, scope/authority readonly;
a fonte recusa referência antiga isolada e pin ausente.

A limpeza desmonta o segundo disco e remove o ponto de montagem. Subvolumes de
laboratório permanecem apenas dentro desse disco descartável, desligado. Contas,
PAM, DBus, Polkit e modo SELinux são comparados antes/depois, sem alterações.
SELinux está Permissive no ensaio, portanto isto **não** qualifica integração MAC.
O driver confirma shutdown normal, QEMU exit 0 e backing raw idêntico em hash,
tamanho, inode e mtime. Scripts/recibos/fontes vinculados em `manifest.json`.

Tentativa anterior: o serial de 26 bytes `lyra-reference-domain-test` foi truncado
pelo virtio, e o guard recusou antes de formatar. VM interrompida e desligada;
registro preservado em analysis/2026-10-06/vegad-parental-btrfs-domain/attempt-1.
A segunda tentativa também recusou antes de formatar: faltava btrfs-progs no guest.
A preparação instalou somente no overlay guest três RPMs oficiais Leap 16.1,
assinaturas conferidas, sem atualização de host. Como a VM de laboratório não
resolve DNS, o host baixou os RPMs oficiais e os transferiu para o guest.
O primeiro ensaio com Btrfs recusou o layout não montado e revelou a ordem incorreta
dos argumentos da propriedade ro. Ajustes feitos, dados/recibos recusados preservados
em attempt-2/attempt-3, VM desligada normalmente após diagnóstico/limpeza.
A execução final usa outro disco Btrfs vazio, serial de 20 bytes e o overlay de
laboratório já preparado. Seus mounts root/authority são bind mounts das raízes
dos subvolumes. O teste desmonta/remonta root durante a restauração e mantém
uma referência em outra árvore. continue-run.py descreve a preparação/execução
final; host-run.py registra a preparação inicial. Transferências usam gzip e
blocos de 64KiB para respeitar o limite do argv. As recusas anteriores não são
apresentadas como passes nem como qualificação MAC/Snapper.

Este ensaio qualifica somente o domínio de snapshot/restauração de **um**
subvolume Btrfs e o leitor root. Não qualifica Snapper na instalação distribuída,
emissão completa/fresca, provisionamento, autorização/publicação de recuperação,
rollback de filesystem/VM inteiro ou controle parental funcional. A estação
local não recebeu pacote, montagem, alteração de fstab ou subvolume.

Pacotes de preparação e URLs oficiais: `packages.json`; verificações de assinatura
no host/guest e instalação original ficam em recibos separados. A preparação da
VM precede o baseline do ensaio; portanto o resultado sem mudanças cobre a execução
do teste, não afirma que a preparação deixou o overlay sem pacotes adicionais.
O disco descartável anterior não é reutilizado no ensaio aprovado.
