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

# Regra polkit de administração remota: sintaxe e invariantes de segurança.
rules=packaging/polkit-1/rules.d/50-vegad-remote-admin.rules
# O contêiner do CI não tem node; quando ele existe (estações de trabalho),
# analisar o fonte como corpo de função valida a sintaxe sem executar nada.
# Sempre: chaves e parênteses balanceados e a assinatura esperada da regra.
if command -v node >/dev/null; then
  node -e 'new Function(require("fs").readFileSync(process.argv[1], "utf8"))' "$rules"
fi
grep -Fq 'polkit.addRule(function(action, subject) {' "$rules"
code="$(sed 's,//.*$,,' "$rules")"
for pair in '{ }' '( )'; do
  set -- $pair
  opened="$(printf '%s' "$code" | tr -cd "$1" | wc -c)"
  closed="$(printf '%s' "$code" | tr -cd "$2" | wc -c)"
  if [ "$opened" -ne "$closed" ]; then
    echo "unbalanced $1$2 in $rules ($opened vs $closed)" >&2
    exit 1
  fi
done
grep -Fq 'if (subject.user === "vega-web")' "$rules"
grep -Fq 'return polkit.Result.NO;' "$rules"
grep -Fq 'subject.isInGroup("wheel")' "$rules"
grep -Fq 'if (subject.local && subject.active)' "$rules"
grep -Fq 'return polkit.Result.AUTH_SELF;' "$rules"
if grep -Eq 'AUTH_ADMIN_KEEP' "$rules" ||
   [ "$(grep -c 'AUTH_SELF_KEEP' "$rules")" -ne 1 ] ||
   ! grep -B1 -F 'return polkit.Result.AUTH_SELF_KEEP;' "$rules" | grep -Fq 'logs.read-admin'; then
  echo "a retained authorization is only allowed for logs.read-admin (desktop parity)" >&2
  exit 1
fi
# YES only for the silent reads the desktop session also has: the brokered
# *.read-admin actions and the network check (add-cli-diagnostics).
yes_condition="$(grep -B2 -F 'return polkit.Result.YES;' "$rules")"
if [ "$(grep -c 'polkit.Result.YES' "$rules")" -ne 1 ] ||
   ! printf '%s' "$yes_condition" | grep -Fq '(backup|kernel|snapshots|firewall|hardware|storage)\.read-admin$/.test(action.id) ||' ||
   ! printf '%s' "$yes_condition" | grep -Fq 'action.id === "org.lyraos.vega.network.diagnose")'; then
  echo "polkit.Result.YES is only allowed for the brokered *.read-admin actions and network.diagnose" >&2
  exit 1
fi
# Every action the rule lets through silently must be silent on the desktop
# too (allow_active yes): the remote rule may not grant more than that.
for action in hardware.read-admin storage.read-admin network.diagnose; do
  awk -v id="org.lyraos.vega.$action" '$0 ~ "action id=\""id"\"" {found=1} found && /<allow_active>/ {print; exit}' packaging/org.lyraos.vega.policy | grep -Fq '<allow_active>yes</allow_active>' || {
    echo "$action must be allow_active yes before the remote rule makes it silent" >&2
    exit 1
  }
done
grep -Fq 'polkit-1/rules.d/50-vegad-remote-admin.rules' packaging/vegad.spec
grep -Fq 'polkit-1/rules.d/50-vegad-remote-admin.rules' packaging/vegad.obs.spec
grep -Fx 'Name=org.lyraos.Vega1' packaging/org.lyraos.Vega1.service >/dev/null
grep -Fx 'BusName=org.lyraos.Vega1' packaging/vegad.service >/dev/null
systemd-analyze verify \
  packaging/vegad.service \
  packaging/vegad-log-export.service \
  packaging/vegad-update-check.service \
  packaging/vegad-first-update.service \
  packaging/vegad-trusted-keys.service \
  packaging/*.timer
rpmspec -P packaging/vegad.spec >/dev/null
rpmspec -P packaging/vegad.obs.spec >/dev/null
rpmspec -P packaging/lyra-nvidia/lyra-nvidia.spec >/dev/null

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
