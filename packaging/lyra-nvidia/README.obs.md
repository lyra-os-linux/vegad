# Publishing lyra-nvidia to OBS

Package `home:rodrigosbrito:nvidia/lyra-nvidia`. The OBS package holds a
copy of the files in this directory; this directory is the source of truth.

```sh
osc checkout home:rodrigosbrito:nvidia lyra-nvidia
cp packaging/lyra-nvidia/{lyra-nvidia.spec,lyra-nvidia.changes,README.md,LICENSE,lyra-nvidia-upstream.repo,60-lyra-nvidia-pm.conf} home:rodrigosbrito:nvidia/lyra-nvidia/
cd home:rodrigosbrito:nvidia/lyra-nvidia && osc addremove && osc commit -m "Update to $(sed -n 's/^Version: *//p' lyra-nvidia.spec)"
```

Bump `Version:` here together with `distro.NvidiaVersion` in
`internal/distro/nvidia.go`; vegad refuses a solver plan whose `lyra-nvidia`
edition does not match that constant.
