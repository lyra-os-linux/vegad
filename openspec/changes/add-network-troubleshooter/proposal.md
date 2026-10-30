# add-network-troubleshooter: parte do vegad

Proposta-mãe:
[lyra-ecosystem/openspec/changes/add-network-troubleshooter](https://github.com/lyra-os-linux/lyra-ecosystem/tree/main/openspec/changes/add-network-troubleshooter),
aprovada em 29/10/2026.

- Teste `network` (`internal/diagnostics/network_check.go`): enlace e sinal
  (`nmcli`), endereço global (`ip -j addr`), gateway, DNS (`getent ahosts` no
  host de checagem do NetworkManager) e internet
  (`nmcli networking connectivity check`). Os passos ficam na métrica `steps`.
- Remédios `reconnect`, `renew-dhcp` e `temporary-dns`, só por `nmcli`, no
  dispositivo indicado no relatório, sem snapshot (não mexem em arquivos).

Escolhas de implementação, dentro dos requisitos:

- **Sem senha:** `Start`, `Cancel` e `Report` de um job que é só o teste
  `network` (e os remédios dele) usam a ação nova
  `org.lyraos.vega.network.diagnose` (`allow_active` `yes`). Os consertos
  continuam no `org.lyraos.vega.hardware.repair`.
- **Gateway:** `ping` quando instalado; senão, a tabela de vizinhos
  (`ip -j neigh`). Muitos roteadores ignoram ping, então o gateway só vira o
  achado principal se o DNS ou a internet também falharem; senão fica como
  informação (`network.gateway-no-ping`).
- **Wi-Fi desligado:** o `reconnect` liga o rádio (`nmcli radio wifi on`)
  antes de conectar.
- **Redação:** o SSID e os endereços entram em `Sensitive` e saem do relatório
  redigido.
