# Empacotamento para o openSUSE Build Service. Variante de packaging/vegad.spec
# adaptada só no Source0/%setup pra bater com o tarball que packaging/_service
# (tar_scm) gera — nome com sufixo de versão e diretório interno próprio, ao
# invés do tar "achatado" usado pelo empacotamento local. Resto do spec é
# idêntico ao de packaging/vegad.spec.
#
# Version literal (não %%{version}/%%define) — o serviço set_version deste
# diretório faz substituição textual simples na linha "Version:" e não
# entende macro, então precisa achar um valor literal aqui pra reescrever.

Name:           vegad
Version:        0
Release:        1%{?dist}
Summary:        Daemon privilegiado do Vega, centro de controle para Linux
License:        GPL-3.0-only
URL:            https://github.com/lyra-os-linux/vegad
Source0:        vegad-src-%{version}.tar
# vendor.tar.gz gerado pelo _service go_modules (rede exigida, que a VM de
# build do OBS não tem — sem isso, "go build" trava tentando baixar
# github.com/godbus/dbus/v5 do proxy.golang.org e falha).
Source1:        vendor.tar.gz

BuildRequires:  go
BuildRequires:  checkpolicy
BuildRequires:  policycoreutils
Requires:       systemd
Requires:       dbus-1
Requires:       polkit
Requires:       pciutils
Requires:       usbutils
Requires:       mokutil
Requires:       dracut
Requires:       snapper
Requires:       util-linux
Requires(post):   systemd
Requires(preun):  systemd
Requires(postun): systemd

Recommends:     NetworkManager
Recommends:     restic
Recommends:     firewalld
Recommends:     fwupd
Recommends:     bluez
Recommends:     logrotate

%description
Daemon privilegiado do Vega para openSUSE Leap. Expõe operações de sistema
(pacotes via Zypper, snapshots Btrfs/Snapper, kernel, hardware, rede,
firewall, usuários) via D-Bus, autorizadas por polkit. Ativado sob demanda
pelo D-Bus (Type=dbus), não roda como serviço permanente.

%prep
%setup -q -n vegad-src-%{version}
# vendor/ precisa ficar irmão do go.mod, na raiz do tarball — é onde
# -mod=vendor abaixo espera encontrá-lo.
tar xzf %{SOURCE1}

%build
GOFLAGS=-mod=vendor go build -trimpath -ldflags "-X github.com/lyraos/vegad/internal/version.Version=%{version}" \
  -o vegad ./cmd/vegad

# Módulo SELinux da issue #118: init_t (domínio do vegad, ainda sem
# domínio dedicado) não tem permissão de escrita em bootloader_etc_t na
# política padrão do openSUSE, então Kernel.ApplyBootConfig falhava com
# "permission denied" em /etc/default/grub. Regra mínima (init_t +
# bootloader_etc_t + write), carregada condicionalmente em %post — ver o
# comentário no próprio .te para o porquê de não ser um domínio dedicado.
checkmodule -M -m -o packaging/selinux/vegad_bootloader.mod \
  packaging/selinux/vegad_bootloader.te
semodule_package -o packaging/selinux/vegad_bootloader.pp \
  -m packaging/selinux/vegad_bootloader.mod

%install
install -Dm755 vegad %{buildroot}%{_prefix}/lib/vega/vegad
install -Dm644 packaging/vegad.service \
  %{buildroot}%{_prefix}/lib/systemd/system/vegad.service
install -Dm644 packaging/vegad-update-check.service \
  %{buildroot}%{_prefix}/lib/systemd/system/vegad-update-check.service
install -Dm644 packaging/vegad-update-check.timer \
  %{buildroot}%{_prefix}/lib/systemd/system/vegad-update-check.timer
install -Dm644 packaging/vegad-update-check-retry.timer \
  %{buildroot}%{_prefix}/lib/systemd/system/vegad-update-check-retry.timer
install -Dm644 packaging/vegad.conf \
  %{buildroot}%{_sysconfdir}/vega/vegad.conf
install -Dm644 packaging/profiles/desktop.conf \
  %{buildroot}%{_datadir}/vega/profiles/desktop.conf
install -Dm644 packaging/profiles/server.conf \
  %{buildroot}%{_datadir}/vega/profiles/server.conf
install -Dm644 packaging/org.lyraos.Vega1.conf \
  %{buildroot}%{_datadir}/dbus-1/system.d/org.lyraos.Vega1.conf
install -Dm644 packaging/org.lyraos.Vega1.service \
  %{buildroot}%{_datadir}/dbus-1/system-services/org.lyraos.Vega1.service
install -Dm644 packaging/org.lyraos.vega.policy \
  %{buildroot}%{_datadir}/polkit-1/actions/org.lyraos.vega.policy
install -Dm644 internal/i18n/catalog/en-US.json \
  %{buildroot}%{_datadir}/locale/en_US/LC_MESSAGES/vegad.json
install -Dm644 internal/i18n/catalog/pt-BR.json \
  %{buildroot}%{_datadir}/locale/pt_BR/LC_MESSAGES/vegad.json
install -Dm644 internal/i18n/catalog/es-ES.json \
  %{buildroot}%{_datadir}/locale/es_ES/LC_MESSAGES/vegad.json

# Exportação periódica do journal do vegad para /var/log/vega/vegad.log —
# journalctl continua sendo a fonte de verdade (o módulo Log do Sistema do
# vega-cli lê o journal direto), isso só mantém uma cópia persistente em
# arquivo, com rotação via logrotate.
install -Dm644 packaging/tmpfiles.d/vega-log.conf \
  %{buildroot}%{_prefix}/lib/tmpfiles.d/vega-log.conf
install -Dm644 packaging/vegad-log-export.service \
  %{buildroot}%{_prefix}/lib/systemd/system/vegad-log-export.service
install -Dm644 packaging/vegad-log-export.timer \
  %{buildroot}%{_prefix}/lib/systemd/system/vegad-log-export.timer
install -Dm644 packaging/logrotate.d/vegad \
  %{buildroot}%{_sysconfdir}/logrotate.d/vegad

install -Dm644 packaging/selinux/vegad_bootloader.pp \
  %{buildroot}%{_datadir}/selinux/packages/vegad_bootloader.pp

%files
%dir %{_prefix}/lib/vega
%{_prefix}/lib/vega/vegad
%{_prefix}/lib/systemd/system/vegad.service
%{_prefix}/lib/systemd/system/vegad-update-check.service
%{_prefix}/lib/systemd/system/vegad-update-check.timer
%{_prefix}/lib/systemd/system/vegad-update-check-retry.timer
%dir %{_sysconfdir}/vega
%config(noreplace) %{_sysconfdir}/vega/vegad.conf
%dir %{_datadir}/vega
%dir %{_datadir}/vega/profiles
%{_datadir}/vega/profiles/desktop.conf
%{_datadir}/vega/profiles/server.conf
%{_prefix}/lib/systemd/system/vegad-log-export.service
%{_prefix}/lib/systemd/system/vegad-log-export.timer
%{_prefix}/lib/tmpfiles.d/vega-log.conf
%config(noreplace) %{_sysconfdir}/logrotate.d/vegad
%{_datadir}/dbus-1/system.d/org.lyraos.Vega1.conf
%{_datadir}/dbus-1/system-services/org.lyraos.Vega1.service
%{_datadir}/polkit-1/actions/org.lyraos.vega.policy
%lang(en) %{_datadir}/locale/en_US/LC_MESSAGES/vegad.json
%lang(pt_BR) %{_datadir}/locale/pt_BR/LC_MESSAGES/vegad.json
%lang(es) %{_datadir}/locale/es_ES/LC_MESSAGES/vegad.json
%dir %{_datadir}/selinux
%dir %{_datadir}/selinux/packages
%{_datadir}/selinux/packages/vegad_bootloader.pp

# vegad.service não tem [Install] (bus-activated, não systemctl enable) —
# só as timers de update-check e log-export são habilitadas aqui.
#
# O módulo SELinux só é carregado se o sistema tiver SELinux habilitado
# (selinuxenabled) e as ferramentas certas instaladas — máquinas sem
# SELinux (a maioria das instalações openSUSE, que usa AppArmor por
# padrão) simplesmente pulam essa parte sem erro.
%post
systemd-tmpfiles --create %{_prefix}/lib/tmpfiles.d/vega-log.conf 2>/dev/null || true
systemctl daemon-reload
systemctl reload dbus.service 2>/dev/null || true
systemctl enable --now vegad-update-check.timer 2>/dev/null || true
systemctl enable --now vegad-log-export.timer 2>/dev/null || true
if command -v semodule >/dev/null 2>&1 && command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled 2>/dev/null; then
  semodule -i %{_datadir}/selinux/packages/vegad_bootloader.pp 2>/dev/null || true
fi

%preun
if [ "$1" = "0" ]; then
  systemctl disable --now vegad-update-check.timer 2>/dev/null || true
  systemctl stop vegad-update-check-retry.timer 2>/dev/null || true
  systemctl disable --now vegad-log-export.timer 2>/dev/null || true
fi

%postun
systemctl daemon-reload
systemctl reload dbus.service 2>/dev/null || true
if [ "$1" = "0" ] && command -v semodule >/dev/null 2>&1; then
  semodule -r vegad_bootloader 2>/dev/null || true
fi

%changelog
