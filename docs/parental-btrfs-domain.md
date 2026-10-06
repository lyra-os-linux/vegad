# Separação da referência para rollback de um subvolume Btrfs

`BtrfsSnapshotDomain` fornece o callback obrigatório de `FileReferenceSource`.
O escopo é explícito: snapshot e restauração **somente do subvolume configurado**.
Não configura Snapper, não instala uma autoridade e não habilita controle parental.

O Btrfs não inclui o conteúdo de outros subvolumes no snapshot de um subvolume.
`inspect-internal rootid` identifica a árvore que contém um arquivo/diretório.
Fontes primárias: [subvolumes e snapshots](https://btrfs.readthedocs.io/en/latest/btrfs-subvolume.html),
[rootid](https://btrfs.readthedocs.io/en/latest/btrfs-inspect-internal.html),
[filesystem show](https://btrfs.readthedocs.io/en/latest/btrfs-filesystem.html) e
[propriedade ro](https://btrfs.readthedocs.io/en/latest/btrfs-property.html).

## Contrato

A configuração confiável fixa um caminho absoluto canônico para a raiz do
subvolume restaurável, montado em seu caminho. O catálogo precisa estar dentro desse caminho e na mesma
árvore Btrfs; a referência precisa estar na raiz de **outro subvolume montado**, no mesmo
filesystem. Esse layout conservador recusa outras configurações, mesmo que possam
ser seguras mediante qualificação adicional. Pastas com nomes diferentes,
`/var/lib` e IDs de mount diferentes não são evidência suficiente.

Os diretórios são abertos com NOFOLLOW/NONBLOCK, mantidos abertos durante as
consultas e precisam pertencer a root. Catálogo e autoridade exigem 0700; a raiz
não pode permitir escrita por grupo/outros. A raiz e a autoridade devem ter inode
256, correspondente a uma raiz de subvolume real, e ser graváveis. Árvores vazias
(inode 2), mounts somente leitura, propriedade `ro=true`, ausência de diretórios,
filesystem diferente, catálogo em outro subvolume e caminhos não canônicos recusam.

O helper existente `/usr/sbin/btrfs` recebe argv fixo, sem shell, ambiente mínimo
LC_ALL=C e o diretório aberto como `/proc/self/fd/3` via ExtraFiles. Há prazo único
de cinco segundos por qualificação, WaitDelay de 100ms e limite de 4096 bytes por
saída. UUID é extraído do cabeçalho conhecido de `filesystem show --raw`; árvore
é um inteiro decimal canônico e propriedade somente leitura exige `ro=false`.
Formato inesperado, erro, helper ausente ou prazo expirado recusam. Esse limite
pode recusar filesystems com muitas linhas/dispositivos; não há fallback permissivo.
Operações de filesystem/kernel ainda dependem do funcionamento do armazenamento;
o prazo do processo não promete interromper uma falha de kernel em estado D.

Após as consultas, paths ainda precisam corresponder aos descritores e o
namespace de mount precisa ser o mesmo. O fingerprint SHA256 contém versão do
contrato, caminhos escapados, UUID de filesystem, IDs de árvores, dispositivos,
inodes e namespace. Não inclui geração/transação, que muda com escritas normais.
A fonte compara observações antes/depois da leitura sob lease SH. Uma nova árvore
restaurada produz outro fingerprint; ele deve permanecer estável durante a leitura,
não necessariamente entre dois boots/restaurações autorizados.

Configuração, ancestrais, helper do sistema, root e operações de mount são
confiáveis. Não há resistência a root hostil, ABA intencional de mounts nem a
restauração conjunta da autoridade, do filesystem ou da VM inteira. O UUID/ID/hash
não é um contador monotônico global. A independência vale **apenas** para o escopo
de um único subvolume; é necessário qualificar o procedimento efetivo de Snapper,
a configuração distribuída e qualquer outro mecanismo de recuperação antes de
ativar a fonte no produto. A estação atual tem `/var/lib` na árvore raiz e não
recebeu subvolumes, mounts, fstab ou pacote novo nesta etapa.

## Validação

Testes comuns verificam parsers fechados, bounds, cancelamento, limites de caminhos
e recusa sem provisionamento. O teste `TestBtrfsDomainRootDisposableVM` só executa
com flag explícita, root, marcador do kernel, serial do segundo disco descartável
e mount Btrfs `/dev/vdb` no diretório fixo do laboratório.

O driver identifica overlay da VM e segundo disco qcow2 novo de 512MiB. Antes de
formatar somente `/dev/vdb`, confirma serial `lyra-ref-domain-test`, tamanho,
ausência de mounts e assinaturas de filesystem. O disco original é backing
somente leitura; nenhum disco do host é formatado/montado. Os scripts e recibos
estão em [parental-btrfs-domain](evidence/parental-btrfs-domain/README.md).

A emissão de referência no ensaio é uma fixture root explícita. Ela permite
qualificar o leitor e o domínio reais, sem afirmar emissão autenticada completa,
provisionamento, journal, guardian, Polkit, autorização de restauração ou aplicação
de política. A sequência do ensaio deve comprovar snapshot real, restauração do
catálogo anterior, preservação do pin/referência atuais, diagnóstico bloqueado,
recusa de referência antiga isolada, mesmo subvolume, catálogo em árvore aninhada,
subvolumes somente leitura e pin ausente. O driver desmonta o segundo disco,
confere contas/PAM/DBus/Polkit/SELinux sem mudanças e desliga a VM.

## Próximo gate

Qualificar o escopo efetivo de rollback/Snapper e a receita de armazenamento
independente, sem presumir o layout da base. Depois, implementar provisionamento e
emissão transacional da referência com autoridade, manutenção e retenção explícita
de proprietários/tombstones. O publisher de bootstrap/recuperação deve revalidar
sob leases e começar bloqueado; o diagnóstico atual não é autorização para gravar.
