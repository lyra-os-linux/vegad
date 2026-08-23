# vegad

Daemon privilegiado do [Vega](https://github.com/lyra-os-linux/vega), centro
de controle para openSUSE Leap. Expõe operações de sistema (pacotes via
Zypper, snapshots Btrfs/Snapper, kernel, hardware, rede, firewall, usuários)
via D-Bus (`org.lyraos.Vega1`), autorizadas por polkit. Ativado sob demanda
pelo D-Bus (`Type=dbus`), não roda como serviço permanente.

O contrato D-Bus que este daemon implementa vive em
[`lyra-vega-dbus`](https://github.com/lyra-os-linux/lyra-vega-dbus)
(`dbus/org.lyraos.Vega1.*.xml`) e deve ser mantido em sincronia com
`internal/dbusserver/*.go`. Os frontends que consomem esse contrato são
[`vega-gtk`](https://github.com/lyra-os-linux/vega),
[`vega-cli`](https://github.com/lyra-os-linux/vega-cli) e
[`vega-web`](https://github.com/lyra-os-linux/vega-web).

Ver [`docs/vegad-profiles.md`](docs/vegad-profiles.md) para os perfis
`desktop`/`server`.

## Build

```sh
go build -trimpath -o vegad ./cmd/vegad
```

## Empacotamento

`packaging/vegad.spec` (build manual/local) e `packaging/vegad.obs.spec` +
`packaging/_service` (openSUSE Build Service). Ver `scripts/dev-install.sh`
para instalar um build local como o daemon do sistema, e
`scripts/test-dbus-integration.sh` para os testes de integração (precisa de
um checkout irmão de `lyra-vega-dbus`).

Licenciado sob GPL-3.0.
