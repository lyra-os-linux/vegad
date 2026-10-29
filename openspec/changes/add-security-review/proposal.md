# add-security-review: parte do vegad

Proposta-mãe, requisitos e design:
[lyra-ecosystem/openspec/changes/add-security-review](https://github.com/lyra-os-linux/lyra-ecosystem/tree/main/openspec/changes/add-security-review),
validada pelo mantenedor em 29/10/2026.

Este repositório implementa:

- `Firewall.ListListening`. `internal/diagnostics/network.go` lê o `ss`,
  classifica o alcance e cruza com as zonas e os serviços do firewalld;
- o teste `security` (`internal/diagnostics/security.go`);
- os remédios `enable-firewall`, `close-port` e `apply-security-patches`
  (`remedy.go`).

O contrato está no lyra-vega-dbus.
