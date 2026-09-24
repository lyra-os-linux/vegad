# Preparação dos repositórios (primeiro boot)

`vegad-first-update.service` roda `vegad first-update` uma única vez no
primeiro boot de um Lyra OS Desktop instalado:

1. confere que `/usr/share/vega/keys/lyra-package-signing-keyring.asc` contém
   exatamente os fingerprints fixados em `internal/distro/trusted_keys.go` e
   importa essas chaves no RPM (`rpmkeys --import`);
2. `zypper --non-interactive refresh` em todos os repositórios;
3. lista as atualizações nativas disponíveis usando os metadados recém-atualizados;
4. publica o estado em `/var/lib/vega/update-status.json` e os sinais D-Bus
   de atualizações disponíveis;
5. grava `/var/lib/vega/first-update.done` após o sucesso dessas etapas.

A instalação de pacotes fica no fluxo normal do Vega. Esta rotina não executa
`zypper update` nem cria snapshots. A publicação do estado não faz outro
refresh nem consulta Flatpak: preserva sua última contagem conhecida, quando
existente. A verificação periódica continua atualizando as duas contagens.

Os nomes `first-update` do comando, da unit e do marcador são preservados por
compatibilidade com as instalações existentes.

O Zypper nunca recebe `--gpg-auto-import-keys`. Uma chave fora da lista fixada
faz o refresh falhar. O encaminhamento dessa falha à aprovação no Vega é
acompanhado na [issue #58](https://github.com/lyra-os-linux/vegad/issues/58).

## Quando roda

- Só com `/usr/lib/lyra-os/release` presente (imagem Lyra OS) e perfil
  `desktop`. Instalar o vegad num openSUSE Leap existente não dispara nada.
- Nunca na sessão live (`rd.live.image`/`root=live:` na linha de comando do
  kernel).
- A unit é habilitada no `%post` apenas na primeira instalação do pacote (caso
  da imagem da ISO). Upgrades preservam a preparação pendente ou concluída.
- Ao atualizar uma versão antiga que ainda não incluía essa unit, o `%pre`
  registra a dispensa em `/var/lib/vega/first-update.skipped`. A presença da
  unit anterior é verificada antes da instalação dos novos arquivos. Essa
  dispensa vale também para a execução manual de `vegad first-update`.
- `first-update.done` é gravado pela rotina após o sucesso; o RPM não cria
  esse marcador. Marcadores de versões anteriores são preservados, pois não
  é possível distinguir com segurança sucesso real de dispensa antiga.
- É `Type=exec`: o boot e o login não esperam a preparação terminar.

## Vega durante a preparação

Enquanto a unit está ativa, o vegad reserva as operações nativas para a preparação. O vegad recusa
logo de início instalar, remover ou atualizar pacotes nativos, limpar o cache
e mexer em repositórios, antes de pedir senha ao Polkit ou de criar um
snapshot, com o erro `org.lyraos.Vega1.Error.FirstUpdateInProgress`: "O sistema
está preparando os repositórios. Tente novamente em alguns minutos."
Buscas, detalhes e listas de atualizações que esbarrarem no lock (saída 7 do
Zypper) recebem a mesma mensagem. Operações Flatpak não usam o lock e seguem
normalmente. Se o lock for de outra ferramenta, a mensagem original do Zypper
é mantida.

## Falhas

Sem o marcador, a unit tenta de novo a cada 15 minutos (até 5 vezes em 3 horas)
e depois no próximo boot. O Zypper espera até 10 minutos por um lock
(`ZYPP_LOCK_TIMEOUT=600`) mantido pelo Vega ou por outra ferramenta.

```sh
journalctl -u vegad-first-update.service
# repetir manualmente (ou optar pela preparação em uma instalação dispensada)
sudo rm -f /var/lib/vega/first-update.done /var/lib/vega/first-update.skipped
sudo systemctl start vegad-first-update.service
```

## Chaves fixadas

SUSE 16, openSUSE Project, openSUSE Backports (duas), SUSE/SuSE legadas,
`home:rodrigosbrito` (lyra/vega/fina) e Packman. Ao trocar uma chave, atualize
o keyring e a lista em Go juntos; `TestShippedKeyringMatchesPinnedFingerprints`
falha se divergirem.

## Manutenção das chaves após o primeiro boot

`vegad-trusted-keys.service` executa `vegad sync-trusted-keys` em cada boot
instalado do Lyra OS Desktop e é enfileirado pelo RPM após instalação ou
upgrade. O scriptlet usa `systemctl --no-block start`: não espera pela
importação enquanto a própria transação RPM pode estar segurando o banco.
Uma falha é tentada novamente após cinco minutos, com limite de cinco
inícios por hora; o próximo boot também tenta novamente.

Essa rotina é independente de `first-update.done` e `first-update.skipped`.
Ela verifica o conjunto exato de fingerprints em toda execução e importa o
keyring autorizado. Reimportar chaves já presentes é idempotente no RPM;
não é necessário um marcador de versão que possa ficar desatualizado em
relação ao banco. Não há refresh, instalação de pacotes nem alteração dos
marcadores da preparação. A sessão live e o perfil server são excluídos.

### Rotação e retenção

Para introduzir uma chave, publique o keyring e a lista de fingerprints na
mesma versão do vegad, antes de o repositório depender exclusivamente da
nova chave. Durante a transição, mantenha ambas as chaves no conjunto
aprovado; posteriormente, a antiga pode ser retirada dos dois arquivos.

A manutenção é aditiva: retirar uma chave do keyring impede novas
importações dessa chave, mas **não a remove do banco RPM**. Chaves antigas
ou adicionadas pelo administrador podem atender outros repositórios e são
preservadas. Revogação/remoção exige uma migração específica e revisada;
remover uma entrada da lista não constitui revogação. Um downgrade também
não apaga chaves previamente importadas.

Diagnóstico e nova tentativa:

```sh
journalctl -u vegad-trusted-keys.service
sudo systemctl restart vegad-trusted-keys.service
```
