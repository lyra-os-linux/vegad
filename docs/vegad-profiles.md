# Perfis do vegad

O `vegad` possui dois perfis de produto definidos pelo administrador do host:

- `desktop`: pacotes nativos, Flatpak, Bluetooth e integrações da sessão gráfica;
- `server`: administração headless e somente pacotes nativos.

O perfil é lido de `VEGAD_PROFILE` no ambiente confiável do serviço ou de
`/etc/vega/vegad.conf`. O valor enviado por clientes D-Bus nunca seleciona o
perfil. Na ausência de configuração, o vegad detecta a edição instalada: o
marcador `/usr/lib/lyra-os/server-release`, renderizado pela imagem do Lyra OS
Server, seleciona `server`; qualquer outra instalação usa `desktop`, preservando
a compatibilidade de instalações anteriores aos perfis. O journal registra a
origem (`VEGAD_PROFILE`, o arquivo, `edition marker …` ou `compatibility
default`).

O pacote instala `/etc/vega/vegad.conf` apenas com comentários
(`%config(noreplace)`); instalações desktop anteriores mantêm o valor explícito
que já tinham. Para fixar o perfil independentemente da detecção, copie um
modelo para o arquivo:

```ini
VEGAD_PROFILE=server
```

Modelos também são instalados em `/usr/share/vega/profiles/`. Depois de mudar
o arquivo, reinicie `vegad.service`; o daemon registra no journal o perfil e a
origem da configuração. A interface `org.lyraos.Vega1.Metadata` publica
`Profile`, `Version` e `Capabilities` para descoberta pelos clientes.

No perfil server, operações cuja origem é `flathub` falham com
`org.lyraos.Vega1.Error.CapabilityUnavailable`; buscas, inventários,
atualizações e limpeza de cache não executam Flatpak. A interface Bluetooth
também não é exportada.

O timer desktop faz a primeira verificação uma hora após o boot, repete a
consulta a cada 24 horas e salva o último resultado em
`/var/lib/vega/update-status.json`. No server, o painel inicial da
CLI e a primeira página após login na Web consultam `RequestUpdateCheck`,
garantindo um aviso por sessão administrativa sem instalar nada
automaticamente. A operação responde imediatamente com o estado persistido e,
quando ele tem mais de cinco minutos, inicia uma verificação assíncrona. Logins
concorrentes compartilham a mesma verificação. O estado contém horário, perfil,
contagens nativa, Flatpak, total e de segurança (zero quando o backend não
classifica patches), além de indicar andamento e o último erro.
Uma falha de rede ou de repositório agenda nova tentativa em duas horas, com
até cinco minutos de atraso aleatório, sem alterar o ciclo diário normal.

Em sessões remotas (SSH, vega-web), a regra polkit
`50-vegad-remote-admin.rules` pede a própria senha de membros de `wheel` para
ações administrativas e libera em silêncio as leituras `*.read-admin`; ver
`privilege-boundaries.md`.

Desktop e Server anunciam `diagnostics-v1` (testes de saúde, vegad#98) e
`diagnostics-v2` (reparo dos achados consertáveis), `diagnostics-v3` (teste de
segurança) e `network-exposure-v1` (`Firewall.ListListening`). No Server, SSH e
a porta 9090 são exposições esperadas.

## NVIDIA (removida na 5.7.0)

O Vega não instala, diagnostica nem recupera mais o driver NVIDIA
(lyra-ecosystem#50). `InstallNvidia`, `NvidiaStatus`, `NvidiaRecovery`,
`CheckNvidia` e `SwitchNvidiaDriver` continuam no barramento só por
compatibilidade e respondem `org.freedesktop.DBus.Error.NotSupported`; nenhum
perfil anuncia `nvidia-official-v1` nem `nvidia-recovery-v1`. Para instalar o
driver, siga o procedimento oficial do openSUSE.

A atualização não apaga nada do que versões anteriores criaram. Esses arquivos
passam a ser do administrador, que pode removê-los se quiser:

- `/etc/systemd/sleep.conf.d/90-lyra-nvidia-quarantine.conf`: quarentena de
  suspensão (só em notebook híbrido com a 580.159.03). Começa com
  `# Managed by Vega`. Para liberar a suspensão:
  `sudo rm /etc/systemd/sleep.conf.d/90-lyra-nvidia-quarantine.conf && sudo systemctl daemon-reload`.
- `/var/lib/vegad-nvidia/`: registro da recuperação (`recovery`,
  `recovery.json`) e, no Server/ext4, os pontos Restic em `points/REFERÊNCIA`
  (repositório, chave e manifesto). Um ponto ainda pode ser restaurado à mão
  com o `restic`; quando não precisar mais, `sudo rm -rf /var/lib/vegad-nvidia`.
  No Btrfs, o ponto é um snapshot do Snapper (`Before Lyra NVIDIA
  installation`), removível pelo Vega ou com `snapper delete`.
- `/etc/zypp/repos.d/lyra-nvidia.repo`: repositório OBS da integração. O
  pacote `lyra-nvidia` e o projeto OBS `home:rodrigosbrito:nvidia` foram
  descontinuados; a migração para os pacotes oficiais (que remove esse
  repositório, o pacote e o canal CUDA `lyra-nvidia-upstream.repo`) está na
  [Discussion #234](https://github.com/lyra-os-linux/lyraos-desktop/discussions/234).
