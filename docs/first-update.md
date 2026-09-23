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
  da imagem da ISO). Numa atualização do pacote, o `%post` grava o marcador.
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
# repetir manualmente
sudo rm /var/lib/vega/first-update.done
sudo systemctl start vegad-first-update.service
```

## Chaves fixadas

SUSE 16, openSUSE Project, openSUSE Backports (duas), SUSE/SuSE legadas,
`home:rodrigosbrito` (lyra/vega/fina) e Packman. Ao trocar uma chave, atualize
o keyring e a lista em Go juntos; `TestShippedKeyringMatchesPinnedFingerprints`
falha se divergirem.
