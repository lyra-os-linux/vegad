# SPDX-License-Identifier: MIT
Name:           lyra-nvidia
Version:        615.71.09
Release:        0
Summary:        Qualified NVIDIA driver integration for Lyra and openSUSE Leap
License:        MIT
URL:            https://github.com/lyra-os-linux/vegad/tree/main/packaging/lyra-nvidia
Source0:        README.md
Source1:        LICENSE
Source2:        lyra-nvidia-upstream.repo
Source3:        60-lyra-nvidia-pm.conf
BuildArch:      noarch
# libzypp owns /etc/zypp and /etc/zypp/repos.d. It must be present both in
# OBS's directory-ownership check and on systems consuming this repository.
BuildRequires:  libzypp
Requires:       libzypp
Requires:       nvidia-open-driver-G07-signed-cuda-kmp-default = %{version}
Requires:       nvidia-open = %{version}
Requires:       nvidia-common-G07 = %{version}
Requires:       nvidia-compute-G07 = %{version}
Requires:       nvidia-compute-utils-G07 = %{version}
Requires:       nvidia-gl-G07 = %{version}
Requires:       nvidia-video-G07 = %{version}
Requires:       nvidia-modprobe = %{version}
Requires:       nvidia-persistenced = %{version}
# These capabilities identify the alternate unsigned/DKMS providers. The signed
# SUSE KMP also provides nvidia-open-driver-G07, so that name MUST NOT conflict.
Conflicts:      nvidia-open-driver-G07-kmp
Conflicts:      nvidia-driver-G07-kmp

%description
Opt-in integration profile for NVIDIA 615.71.09 on Leap 16.1 x86_64.
Requires matching upstream NVIDIA userspace and the SUSE-signed CUDA KMP.
Rejects incompatible NVIDIA DKMS providers without blocking unrelated DKMS
users or creating kernel/package locks. Enables fine-grained runtime D3
power management so hybrid notebooks can power off an idle NVIDIA GPU. Includes upstream repository metadata;
proprietary driver binaries are obtained directly from NVIDIA, not this RPM.
This profile does not certify a future kernel or an unsupported GPU.

%prep
%setup -q -c -T
cp %{SOURCE0} README.md
cp %{SOURCE1} LICENSE

%build

%install
install -D -m 0644 %{SOURCE2} %{buildroot}%{_sysconfdir}/zypp/repos.d/lyra-nvidia-upstream.repo
install -D -m 0644 %{SOURCE3} %{buildroot}%{_prefix}/lib/modprobe.d/60-lyra-nvidia-pm.conf

%files
%license LICENSE
%doc README.md
%config(noreplace) %{_sysconfdir}/zypp/repos.d/lyra-nvidia-upstream.repo
%{_prefix}/lib/modprobe.d/60-lyra-nvidia-pm.conf

%changelog
