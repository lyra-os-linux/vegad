# Perfis do vegad

O `vegad` possui dois perfis de produto definidos pelo administrador do host:

- `desktop`: pacotes nativos, Flatpak, Bluetooth e integrações da sessão gráfica;
- `server`: administração headless e somente pacotes nativos.

O perfil é lido de `VEGAD_PROFILE` no ambiente confiável do serviço ou de
`/etc/vega/vegad.conf`. O valor enviado por clientes D-Bus nunca seleciona o
perfil. Na ausência de configuração, `desktop` é usado para preservar a
compatibilidade de instalações existentes.

O pacote desktop instala:

```ini
VEGAD_PROFILE=desktop
```

O pacote do Lyra Server deve instalar `/etc/vega/vegad.conf` com:

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

O timer desktop verifica atualizações a cada quatro horas e salva o último
resultado em `/var/lib/vega/update-status.json`. No server, o painel inicial da
CLI e a primeira página após login na Web consultam `RequestUpdateCheck`,
garantindo um aviso por sessão administrativa sem instalar nada
automaticamente. A operação responde imediatamente com o estado persistido e,
quando ele tem mais de cinco minutos, inicia uma verificação assíncrona. Logins
concorrentes compartilham a mesma verificação. O estado contém horário, perfil,
contagens nativa, Flatpak, total e de segurança (zero quando o backend não
classifica patches), além de indicar andamento e o último erro.
