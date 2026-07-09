#!/usr/bin/env bash
# Builds vegad from the local checkout and installs it as the live system
# daemon, for iterating on vegad changes without a full PKGBUILD/pacman
# roundtrip. Run as your normal user (not via sudo) — it calls sudo itself
# for the individual privileged steps, so you'll get one password prompt.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
vegad_dir="$repo_root/vegad"
packaging_dir="$repo_root/packaging/vegad"

echo "==> Buildando vegad a partir de $vegad_dir"
(
  cd "$vegad_dir"
  go build -trimpath -ldflags "-X github.com/lyraos/vegad/internal/version.Version=dev" -o vegad ./cmd/vegad
)

echo "==> Usuário/diretório vega-build (systemd-sysusers/tmpfiles)"
sudo systemd-sysusers "$packaging_dir/sysusers.d/vega-build.conf"
sudo systemd-tmpfiles --create "$packaging_dir/tmpfiles.d/vega-build.conf"

echo "==> Regra sudoers de vega-build (validada antes de instalar)"
tmp_sudoers="$(mktemp)"
cp "$packaging_dir/sudoers.d/vega-build" "$tmp_sudoers"
if ! sudo visudo -cf "$tmp_sudoers" >/dev/null; then
  echo "Falha: $packaging_dir/sudoers.d/vega-build tem sintaxe inválida, abortando." >&2
  rm -f "$tmp_sudoers"
  exit 1
fi
rm -f "$tmp_sudoers"
sudo install -Dm440 "$packaging_dir/sudoers.d/vega-build" /etc/sudoers.d/vega-build

echo "==> Ações polkit"
sudo install -Dm644 "$packaging_dir/org.lyraos.vega.policy" /usr/share/polkit-1/actions/org.lyraos.vega.policy

echo "==> Instalando binário em /usr/lib/vega/vegad"
sudo install -Dm755 "$vegad_dir/vegad" /usr/lib/vega/vegad

echo "==> Reiniciando vegad.service"
sudo systemctl restart vegad.service

echo "==> Pronto. Status:"
systemctl status vegad.service --no-pager | head -8
