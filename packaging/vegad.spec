# Empacotamento para Linux. Ainda não publicado
# em nenhum repositório oficial (OBS ou similar) — ver packaging/opensuse/
# no topo do repositório para o script de instalação manual equivalente.
#
# %%{version} é passado pela release/CI via `rpmbuild --define "version X.Y.Z"`
# (a tag `vX.Y.Z` sem o "v"). Buildar sem essa define usa o default abaixo.
%{!?version: %define version 0.0.0}

Name:           vegad
Version:        %{version}
Release:        1%{?dist}
Summary:        Daemon privilegiado do Vega, centro de controle para Linux
License:        GPL-3.0-only
URL:            https://github.com/britors/Vega
Source0:        vega-src.tar.gz

BuildRequires:  go
Requires:       systemd
Requires:       dbus-1
Requires:       polkit
Requires(post):   systemd
Requires(preun):  systemd
Requires(postun): systemd

Recommends:     snapper
Recommends:     flatpak
Recommends:     NetworkManager
Recommends:     restic
Recommends:     firewalld
Recommends:     fwupd
Recommends:     bluez

%description
Daemon privilegiado do Vega para openSUSE Leap. Expõe operações de sistema
(pacotes via Zypper, snapshots Btrfs/Snapper, kernel, hardware, rede,
firewall, usuários) via D-Bus, autorizadas por polkit. Ativado sob demanda
pelo D-Bus (Type=dbus), não roda como serviço permanente.

%prep
%setup -q -c -n vega-src

%build
cd vegad
go build -trimpath -ldflags "-X github.com/lyraos/vegad/internal/version.Version=%{version}" \
  -o vegad ./cmd/vegad

%install
install -Dm755 vegad/vegad %{buildroot}%{_prefix}/lib/vega/vegad
install -Dm644 packaging/vegad/vegad.service \
  %{buildroot}%{_prefix}/lib/systemd/system/vegad.service
install -Dm644 packaging/vegad/vegad-update-check.service \
  %{buildroot}%{_prefix}/lib/systemd/system/vegad-update-check.service
install -Dm644 packaging/vegad/vegad-update-check.timer \
  %{buildroot}%{_prefix}/lib/systemd/system/vegad-update-check.timer
install -Dm644 packaging/vegad/org.lyraos.Vega1.conf \
  %{buildroot}%{_datadir}/dbus-1/system.d/org.lyraos.Vega1.conf
install -Dm644 packaging/vegad/org.lyraos.Vega1.service \
  %{buildroot}%{_datadir}/dbus-1/system-services/org.lyraos.Vega1.service
install -Dm644 packaging/vegad/org.lyraos.vega.policy \
  %{buildroot}%{_datadir}/polkit-1/actions/org.lyraos.vega.policy

%files
%{_prefix}/lib/vega/vegad
%{_prefix}/lib/systemd/system/vegad.service
%{_prefix}/lib/systemd/system/vegad-update-check.service
%{_prefix}/lib/systemd/system/vegad-update-check.timer
%{_datadir}/dbus-1/system.d/org.lyraos.Vega1.conf
%{_datadir}/dbus-1/system-services/org.lyraos.Vega1.service
%{_datadir}/polkit-1/actions/org.lyraos.vega.policy

# vegad.service não tem [Install] (bus-activated, não systemctl enable) —
# só a timer de update-check é habilitada aqui, mesma lógica de
# packaging/vegad/vegad.install.
%post
systemctl daemon-reload
systemctl reload dbus.service 2>/dev/null || true
systemctl enable --now vegad-update-check.timer 2>/dev/null || true

%preun
if [ "$1" = "0" ]; then
  systemctl disable --now vegad-update-check.timer 2>/dev/null || true
fi

%postun
systemctl daemon-reload
systemctl reload dbus.service 2>/dev/null || true

%changelog
