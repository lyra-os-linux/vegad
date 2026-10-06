# Vínculo da referência parental com a configuração do Snapper

`NewSnapperSnapshotDomain(config, scope).Qualify` compõe o observador Btrfs real
com leitura segura de uma configuração Snapper confiável. Pode fornecer o callback
obrigatório de FileReferenceSource. Não registra configuração, cria autoridade,
concede manutenção, executa Snapper ou habilita parental no daemon.

A configuração indica o subvolume fotografado. ALLOW_USERS/ALLOW_GROUPS delegam
operações; filtros de comparação não excluem conteúdo dos snapshots. Fontes:
[configurações Snapper](https://manpages.opensuse.org/Tumbleweed/snapper/snapper-configs.5.en.html)
e [snapper(8)](https://manpages.opensuse.org/Tumbleweed/snapper/snapper.8.en.html).
Esses manuais upstream servem de referência; a execução desta etapa usa os RPMs
oficiais Leap 16.1, Snapper 0.12.1/libsnapper 7.4.3.

## Perfil aceito

Configuração e scope são caminhos absolutos canônicos fixados pelo serviço.
O arquivo deve ser regular, root, sem escrita por grupo/outros, sem bits especiais,
um único link, entre 1 e 16384 bytes; o diretório deve ser root sem escrita por
outros. Aceita 0644 ou 0600. Ancestors/root/helper/alterações de topologia são trusted.
Descritores NOFOLLOW/NONBLOCK, limites de leitura e verificações de inode, metadata,
conteúdo e path recusam substituição ou alteração observada durante a consulta.
Nenhum lock/arquivo/diretório é criado. Config writers não passam a ser seguros
contra root hostil por essas verificações; não há promessa de detecção de ABA
intencional. IO depende do armazenamento/kernel, como no observador Btrfs.

Sintaxe suportada é deliberadamente conservadora: UTF-8, comentários em linhas
próprias, atribuições KEY="VALUE", newline final, chaves conhecidas sem duplicatas,
sem source, expansão, escapes ou quoting ambíguo. Valores booleanos conhecidos são
yes/no. Exige explicitamente SUBVOLUME igual ao scope, FSTYPE="btrfs",
ALLOW_USERS="", ALLOW_GROUPS="", SYNC_ACL="no". Outros perfis recusam.
Settings numéricos de limpeza são opacos: o leitor não certifica toda a semântica
ou disponibilidade da configuração, nem sua inscrição em SNAPPER_CONFIGS.
Novas chaves upstream desconhecidas recusam até qualificação, sem fallback.

O fingerprint inclui configuração completa, path, inode/modo, diretório e witness
Btrfs. Assim, até uma mudança de comentário altera o witness entre observações.
Uma configuração válida não substitui a prova da topologia real: continua exigindo
root montada, catálogo na mesma árvore e autoridade em outra raiz de subvolume
montada do mesmo filesystem. Veja [parental-btrfs-domain.md](parental-btrfs-domain.md).

## Ensaio

[Recibos e scripts](evidence/parental-snapper-domain/README.md) comprovam leitura
root real, emissão fixture e criação de snapshot pelo CLI Snapper --no-dbus,
com uma configuração root explícita, privada, não inscrita no fluxo global de timers.
O catálogo gravado no snapshot é comparado byte a byte. Após avanço da referência e
do catálogo, restauração offline Btrfs desse snapshot exige recuperação bloqueada,
com referência/pin atuais preservados. Permissões delegadas recusam; uma mudança
válida da configuração muda o witness. Outras recusas da fonte/domain continuam
cobertas. Estado de contas/PAM/DBus/Polkit/configuração Snapper e SELinux não muda
durante o ensaio; configuração temporária removida e disco desmontado.

A VM usa overlay novo sobre o overlay de laboratório já preparado, backing raw
preservado e segundo disco qcow2 vazio serializado de 512MiB. Somente o guest recebe
Snapper oficial antes do baseline. Nenhum pacote, mount, fstab, subvolume ou receita
KIWI da máquina local é modificado. SELinux Permissive não qualifica integração MAC.

## Limites e próximos gates

Foi qualificada criação Snapper e restauração offline de um único subvolume.
**Não** foi executado snapper rollback, troca do subvolume default, reboot no
snapshot, GRUB ou ISO. Montagem persistente de autoridade e .snapshots após rollback
continua gate de distribuição. Não há proteção a restore de filesystem/VM inteiro.
O observador não prova emissão completa/fresca, journal, autorização, guardian,
política ou recuperação publicada. Esses contratos continuam necessários antes de
ativar/instalar parental na estação. A emissão root no ensaio é uma fixture.

O próximo desenvolvimento é provisionamento e emissão transacional da referência,
com manutenção autorizada, retenção de owners/tombstones e publicação durável do pin
junto à referência. O gate de boot/Snapper/armazenamento persistente permanece aberto
para a entrega na ISO; o catálogo não deve autorizar contas por ausência de estado.
