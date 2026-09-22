# Atualização inicial (primeiro boot)

`vegad-first-update.service` roda `vegad first-update` uma única vez no
primeiro boot de um Lyra OS Desktop instalado:

1. confere que `/usr/share/vega/keys/lyra-package-signing-keyring.asc` contém
   exatamente os fingerprints fixados em `internal/distro/trusted_keys.go` e
   importa essas chaves no RPM (`rpmkeys --import`);
2. `zypper --non-interactive refresh` em todos os repositórios;
3. atualiza todos os pacotes pendentes (`zypper update`, repositório por
   repositório, o mesmo caminho da ação "Atualizar tudo" do Vega), entre um par
   de snapshots Snapper pre/post;
4. grava `/var/lib/vega/first-update.done` e atualiza o estado de atualizações
   pendentes (`/var/lib/vega/update-status.json`).

O Zypper nunca recebe `--gpg-auto-import-keys`. Se um repositório apresentar
uma chave fora da lista fixada, o refresh falha e o usuário aprova o
fingerprint completo no Vega, como em qualquer outro repositório.

## Quando roda

- Só com `/usr/lib/lyra-os/release` presente (imagem Lyra OS) e perfil
  `desktop`. Instalar o vegad num openSUSE Leap existente não dispara nada.
- Nunca na sessão live (`rd.live.image`/`root=live:` na linha de comando do
  kernel).
- A unit é habilitada no `%post` apenas na primeira instalação do pacote (caso
  da imagem da ISO). Numa atualização do pacote, o `%post` grava o marcador.
- É `Type=exec`: o boot e o login não esperam a atualização terminar.

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
