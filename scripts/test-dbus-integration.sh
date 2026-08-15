#!/usr/bin/env bash
# Executa os testes marcados como integração contra o vegad recém-compilado
# em um barramento privado, sem substituir o serviço instalado no host.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmpdir="$(mktemp -d)"
integration_gocache="${GOCACHE:-/tmp/vega-integration-go-cache}"
integration_gopath="${GOPATH:-/tmp/vega-integration-gopath}"
vegad_pid=""
bus_pid=""
cleanup() {
  [ -z "$vegad_pid" ] || kill "$vegad_pid" 2>/dev/null || true
  [ -z "$bus_pid" ] || kill "$bus_pid" 2>/dev/null || true
  rm -rf -- "$tmpdir"
}
trap cleanup EXIT INT TERM

for command in busctl cargo dbus-daemon go; do
  command -v "$command" >/dev/null || {
    echo "dependência ausente: $command" >&2
    exit 1
  }
done

mapfile -t bus_info < <(dbus-daemon --session --fork --print-address=1 --print-pid=1)
export DBUS_SYSTEM_BUS_ADDRESS="${bus_info[0]}"
bus_pid="${bus_info[1]}"

(
  cd "$repo_root/vegad"
  GOCACHE="$integration_gocache" GOPATH="$integration_gopath" \
    go build -o "$tmpdir/vegad" ./cmd/vegad
)

VEGAD_PROFILE=server "$tmpdir/vegad" >"$tmpdir/vegad.log" 2>&1 &
vegad_pid=$!

ready=false
for _ in {1..50}; do
  if busctl --address="$DBUS_SYSTEM_BUS_ADDRESS" call org.lyraos.Vega1 \
    /org/lyraos/Vega1 org.lyraos.Vega1.System Ping >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 0.1
done
if [ "$ready" != true ]; then
  echo "vegad privado não iniciou" >&2
  sed -n '1,120p' "$tmpdir/vegad.log" >&2
  exit 1
fi

profile="$(busctl --address="$DBUS_SYSTEM_BUS_ADDRESS" call org.lyraos.Vega1 \
  /org/lyraos/Vega1 org.lyraos.Vega1.Metadata Profile | sed -n 's/^s "\(.*\)"$/\1/p')"
[ "$profile" = server ] || {
  echo "perfil inesperado: $profile" >&2
  exit 1
}

if busctl --address="$DBUS_SYSTEM_BUS_ADDRESS" introspect org.lyraos.Vega1 \
  /org/lyraos/Vega1 | rg -q 'org.lyraos.Vega1.Bluetooth'; then
  echo "Bluetooth foi exportado no perfil server" >&2
  exit 1
fi

cd "$repo_root"
cargo test --locked -p lyra-vega-dbus -- --ignored --test-threads=1
