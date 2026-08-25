#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

for command in xmllint systemd-analyze rpmspec; do
  command -v "$command" >/dev/null || {
    echo "missing contract validator: $command" >&2
    exit 1
  }
done

xmllint --noout packaging/org.lyraos.Vega1.conf packaging/org.lyraos.vega.policy
grep -Fx 'Name=org.lyraos.Vega1' packaging/org.lyraos.Vega1.service >/dev/null
grep -Fx 'BusName=org.lyraos.Vega1' packaging/vegad.service >/dev/null
systemd-analyze verify \
  packaging/vegad.service \
  packaging/vegad-log-export.service \
  packaging/vegad-update-check.service \
  packaging/*.timer
rpmspec -P packaging/vegad.spec >/dev/null
rpmspec -P packaging/vegad.obs.spec >/dev/null

grep -Eq '^type vegad_t;' packaging/selinux/vegad_bootloader.te
grep -Eq '^type vegad_exec_t;' packaging/selinux/vegad_bootloader.te
grep -Eq '^type_transition init_t vegad_exec_t:process vegad_t;' \
  packaging/selinux/vegad_bootloader.te
grep -Eq '^permissive vegad_t;' packaging/selinux/vegad_bootloader.te
grep -Eq '^allow vegad_t bootloader_etc_t:file write;' \
  packaging/selinux/vegad_bootloader.te
if grep -Eq '^allow init_t bootloader_etc_t:' packaging/selinux/vegad_bootloader.te; then
  echo "SELinux policy must not grant bootloader writes to every init_t process" >&2
  exit 1
fi
grep -F '/usr/lib/vega/vegad' packaging/selinux/vegad_bootloader.fc >/dev/null

echo "vegad D-Bus, polkit, systemd and RPM packaging contracts are valid"
