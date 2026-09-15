#!/usr/bin/env bash
# Executa os testes marcados como integração contra o vegad recém-compilado
# em um barramento privado, sem substituir o serviço instalado no host.
#
# Depende de um checkout irmão de lyra-vega-dbus (../lyra-vega-dbus a partir
# deste repo) para rodar os testes --ignored do lado do cliente — desde a
# quebra do monorepo vega, esse crate mora em repositório próprio.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dbus_client_dir="${VEGA_DBUS_CLIENT_DIR:-$repo_root/../lyra-vega-dbus}"
if [ ! -d "$dbus_client_dir" ]; then
  echo "esperava um checkout irmão em $dbus_client_dir (git clone https://github.com/lyra-os-linux/lyra-vega-dbus ../lyra-vega-dbus)" >&2
  exit 1
fi
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

for command in busctl cargo dbus-daemon go pkcheck; do
  command -v "$command" >/dev/null || {
    echo "dependência ausente: $command" >&2
    exit 1
  }
done

# Container CI has no system manager. Exercise the wire contracts with the
# daemon already unprivileged; the real root-to-worker boundary is tested in
# scripts/check-query-vm.py against systemd and Polkit.
chmod 0755 "$tmpdir"
cat > "$tmpdir/bus.conf" <<EOF
<busconfig><type>session</type><listen>unix:path=$tmpdir/bus</listen><auth>EXTERNAL</auth>
<policy context="default"><allow user="*"/><allow own="*"/>
<allow send_destination="*"/><allow receive_sender="*"/></policy></busconfig>
EOF
mapfile -t bus_info < <(dbus-daemon --config-file="$tmpdir/bus.conf" --fork --print-address=1 --print-pid=1)
export DBUS_SYSTEM_BUS_ADDRESS="${bus_info[0]}"
bus_pid="${bus_info[1]}"

(
  cd "$repo_root"
  GOCACHE="$integration_gocache" GOPATH="$integration_gopath" \
    go build -o "$tmpdir/vegad" ./cmd/vegad
)

daemon_command=("$tmpdir/vegad")
if [ "$EUID" = 0 ]; then
  daemon_command=(runuser --user nobody -- "$tmpdir/vegad")
fi
VEGAD_PROFILE=server "${daemon_command[@]}" >"$tmpdir/vegad.log" 2>&1 &
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
  /org/lyraos/Vega1 | grep -q 'org.lyraos.Vega1.Bluetooth'; then
  echo "Bluetooth foi exportado no perfil server" >&2
  exit 1
fi

cd "$dbus_client_dir"
# This private bus deliberately has no Polkit authority. Backup reads now
# require authorization; their successful path and denial are covered with
# real Polkit in check-query-vm.py. Keep the private-bus negative check here.
if busctl --address="$DBUS_SYSTEM_BUS_ADDRESS" call org.lyraos.Vega1 \
  /org/lyraos/Vega1 org.lyraos.Vega1.Backup ListConfigs >"$tmpdir/backup-read.log" 2>&1; then
  echo "leitura protegida de backup foi autorizada sem Polkit" >&2
  exit 1
fi
grep -q 'org.lyraos.vega.backup.read-admin' "$tmpdir/backup-read.log" || {
  cat "$tmpdir/backup-read.log" >&2
  exit 1
}
cargo test --locked -- --ignored --test-threads=1 \
  --skip backup::tests::real_daemon_exposes_the_read_only_backup_contract
