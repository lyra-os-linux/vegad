# remove-nvidia-integration: parte do vegad

Proposta-mãe, requisitos e design:
[lyra-ecosystem/openspec/changes/remove-nvidia-integration](https://github.com/lyra-os-linux/lyra-ecosystem/tree/main/openspec/changes/remove-nvidia-integration)
(lyra-ecosystem#50), aprovada pelo mantenedor em 31/10/2026. Contrato:
lyra-vega-dbus#18.

Este repositório deixa de instalar, diagnosticar e recuperar a pilha NVIDIA:

- `InstallNvidia`, `NvidiaStatus`, `NvidiaRecovery` e `CheckNvidia` seguem o
  `SwitchNvidiaDriver`: ficam exportados com as mesmas assinaturas, como
  `disabledOperation`, e respondem `org.freedesktop.DBus.Error.NotSupported`
  sem polkit, sem worker de consulta e sem tocar no sistema;
- `nvidia-official-v1` e `nvidia-recovery-v1` saem das capacidades dos perfis
  `desktop` e `server`;
- saem `internal/dbusserver/nvidia*.go`, `internal/distro/nvidia*.go`,
  `internal/nvidiarecovery`, o subcomando `vegad nvidia-recover`, a
  reconciliação da quarentena de suspensão no início do daemon, a exceção de
  `ProtectKernelModules` dos workers e `docs/nvidia.md`.

Ficam: o uso da GPU pelo `nvidia-smi` no monitor, a menção a módulos NVIDIA
como esperados no teste `platform`, a detecção genérica da GPU no inventário e
`packaging/lyra-nvidia/` até a decisão sobre o pacote; ele foi descontinuado e
removido em 31/10/2026 (migração manual na Discussion #234).

A atualização não apaga os arquivos que versões anteriores criaram; a lista e
como removê-los estão em `docs/vegad-profiles.md`.
