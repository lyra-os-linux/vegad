# Lyra NVIDIA

Perfil RPM de integração NVIDIA para **Lyra OS baseado no openSUSE Leap 16.1
x86_64** e para **openSUSE Leap 16.1 x86_64**. Canal público de instalação
opcional, com staging separado para futuras atualizações. A integração nas
ISOs depende da qualificação específica de cada imagem.

O pacote aberto exige bibliotecas NVIDIA **615.71.09** e o KMP CUDA
correspondente **assinado pela SUSE**, obtidos dos repositórios de origem.
O OBS hospeda a integração, não os binários proprietários NVIDIA. A fonte
deste pacote é versionada em `packaging/lyra-nvidia/` no repositório
[vegad](https://github.com/lyra-os-linux/vegad), junto da constante
`distro.NvidiaVersion` que o daemon usa para qualificar a instalação.

## O que este pacote resolve

O repositório NVIDIA oferece várias versões de bibliotecas e um provedor DKMS,
enquanto o Leap publica o KMP assinado de uma única versão por kernel. Sem um
contrato, bibliotecas e módulo divergem (por exemplo bibliotecas 615 com KMP
610, como ocorreu na máquina de referência). Este pacote exige versões
correspondentes e rejeita as capacidades exclusivas dos provedores
alternativos NVIDIA sem assinatura SUSE. O provedor assinado
também fornece nomes genéricos NVIDIA; eles não são usados como conflitos.

A atualização para outra versão de driver requer atualizar também o perfil
Lyra após qualificação. Não há locks de kernel ou pacotes, e DKMS de outros
dispositivos não é bloqueado. Essa política exige manutenção: não manter uma
versão antiga indefinidamente quando houver correções de segurança.

O RPM contém somente esta documentação, a licença da integração, a definição
do repositório oficial NVIDIA/SUSE16 com verificação de assinaturas e
`/usr/lib/modprobe.d/60-lyra-nvidia-pm.conf`. Não executa o instalador `.run`,
downloads em scriptlets ou mudanças no bootloader.

## Economia de energia em notebooks híbridos

O padrão do driver (`NVreg_DynamicPowerManagement=0x03`) desativa o RTD3 em
notebooks anteriores ao Ampere, e a GPU NVIDIA fica ligada mesmo ociosa. O
pacote define `0x02` (fine-grained), que permite desligá-la quando não há
trabalho. Para mudar o valor, crie um arquivo de mesmo nome em
`/etc/modprobe.d` e reinicie.

O RTD3 também exige o driver Vulkan do Mesa para a GPU integrada
(`libvulkan_intel` ou `libvulkan_radeon`). Sem ele, o renderizador Vulkan do
GTK 4 roda todos os apps na GPU NVIDIA. O Vega instala esse driver pelos
repositórios do sistema logo após a transação NVIDIA. Ele não é dependência
deste pacote porque a transação NVIDIA usa só o OSS do Leap, cujo Mesa diverge
do instalado depois de uma atualização. Para conferir:
`cat /sys/bus/pci/devices/<slot da GPU>/power/runtime_status` deve mostrar
`suspended` com a GPU ociosa.

## Instalação piloto

Use somente na base Leap 16.1 x86_64 e em GPU suportada pelo driver G07 aberto
(Turing ou posterior, conforme a lista oficial da versão). A referência local
é uma GTX 1650/Turing com kernel 6.12.0-160100.7-default e Secure Boot habilitado.
O KMP assinado existe no OSS do Leap só para o kernel que a distribuição
publica no momento; uma atualização de kernel exige requalificar este perfil
(a versão 610.57.04 ficou sem KMP quando o Leap passou ao kernel 160100.7).
Isso não qualifica todas as placas, notebooks ou próximos kernels.

O repositório OSS do Leap 16.1 deve estar habilitado para fornecer o KMP SUSE.
O metapacote precisa do canal NVIDIA já durante a resolução da primeira
transação: a definição de repositório incluída no RPM só existe após instalá-lo.
Use o canal NVIDIA como fonte adicional temporária nessa primeira instalação:

```sh
sudo zypper addrepo --refresh https://download.opensuse.org/repositories/home:/rodrigosbrito:/nvidia/openSUSE_Leap_16.1/ lyra-nvidia
sudo zypper refresh lyra-nvidia
sudo zypper --plus-repo https://developer.download.nvidia.com/compute/cuda/repos/suse16/x86_64/ install --dry-run --allow-vendor-change lyra-nvidia
sudo zypper --plus-repo https://developer.download.nvidia.com/compute/cuda/repos/suse16/x86_64/ install --allow-vendor-change lyra-nvidia
```

Conferir a transação antes de confirmar: kernel/KMP compatíveis, componentes
NVIDIA 615.71.09 e nenhuma remoção inesperada do ambiente gráfico. A mudança
de fornecedor é explícita e restrita à transação de instalação. Não ignorar
dependências, forçar substituição de arquivos ou aceitar remover o perfil para
resolver uma atualização. Migração de conjuntos antigos pode exigir resolver
conflitos; interrompa se o conjunto completo não estiver disponível.

Conferir as chaves de assinatura pelas fontes oficiais. A chave do canal CUDA
consultada em 15/09/2026 tem fingerprint
`CF65 941A 859D 4B4E 6870 F188 736A 284B 3A8B 5622`.
Não desabilitar verificações GPG. A licença MIT deste pacote não altera as
licenças dos drivers baixados, que devem ser consultadas separadamente.

Após instalar, o arquivo `lyra-nvidia-upstream.repo` mantém o canal oficial
disponível junto com o contrato RPM. Não manter aliases duplicados habilitados
nem outros canais NVIDIA concorrentes sem uma política de migração validada.
O pacote preserva alterações locais no arquivo e não reativa um canal que o
administrador tenha desabilitado.

## Boot, atualização e diagnóstico

Reinicie após mudança efetiva de driver ou kernel. Confira `nvidia-smi`, versão
do módulo carregado/em disco, bibliotecas, assinatura e logs; qualifique também
renderização ou CUDA real conforme a finalidade. Sucesso de resolução RPM não
equivale a sucesso do boot ou de inferência.

**Limite importante:** dependências satisfeitas por um KMP para um kernel
antigo ainda instalado não garantem módulo compatível com o próximo kernel
selecionado no GRUB. Este metapacote não bloqueia atualizações de kernel.
Antes de promover uma combinação, verificar o módulo para o kernel de destino,
initramfs, assinatura Secure Boot e boot real. Em falha, usar o kernel/conjunto
coerente anterior e investigar a atualização; não desabilitar Secure Boot como
correção padrão.

A remoção de `lyra-nvidia` remove o contrato; não é um procedimento completo de
desinstalação do driver. Antes de remover, escolher e conferir o provedor
substituto ou desabilitar o canal NVIDIA, simular a transação e manter recuperação.
Arquivos de configuração modificados podem permanecer como `.rpmsave`; conferir
os repositórios habilitados após a transação. O Server usa ext4: não pressupor
disponibilidade de Snapper nessa edição.

## Qualificação e promoção

Testes de solver devem usar base RPM isolada, sem instalar arquivos no host:
reproduzir bibliotecas de outra versão/DKMS sem perfil, instalar o perfil,
atualizar mantendo 615.71.09, rejeitar bibliotecas isoladas de outra versão,
rejeitar DKMS da mesma versão e resolver instalação limpa.
Registrar resultados com a revisão dos metadados e o RPM testado.

A qualificação para adoção automática nas ISOs exige migração, atualização/recuperação e boot do kernel de
destino, Secure Boot, hardware representativo, consumidores Zypper/PackageKit/
Vega e fluxos das ISOs locais. O piloto básico atual passou em Wayland, NVML e
EGL/OpenGL na GTX 1650; não é prova de estabilidade prolongada, Vulkan, suspensão,
monitor externo ou CUDA/llama.cpp.

## Fontes

- [Driver NVIDIA para SUSE](https://docs.nvidia.com/datacenter/tesla/driver-installation-guide/suse.html)
- [Módulos abertos NVIDIA](https://github.com/NVIDIA/open-gpu-kernel-modules)
- [GPUs suportadas por 615.71.09](https://download.nvidia.com/XFree86/Linux-x86_64/615.71.09/README/supportedchips.html)
- [Política do OBS público](https://en.opensuse.org/openSUSE:Build_Service_application_blacklist)
