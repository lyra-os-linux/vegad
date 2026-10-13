#!/bin/bash
set -euo pipefail

source_initrd=/home/rodrigo/Git/LyraOS/analysis/2026-09-21/parental-selinux/assets/initrd.gz
module_root=/usr/lib/modules/6.12.0-160100.4-default
work=/tmp/lyra-parental-root-initrd-build
output=/tmp/lyra-parental-layout-20261006/initrd-btrfs.gz

test ! -e "$work"
mkdir -m 700 "$work"
gzip -dc "$source_initrd" | (cd "$work" && cpio -idmu --quiet --no-absolute-filenames)
for module in \
  kernel/lib/raid6/raid6_pq.ko.zst \
  kernel/crypto/xor.ko.zst \
  kernel/crypto/blake2b_generic.ko.zst \
  kernel/fs/btrfs/btrfs.ko.zst
do
  mkdir -p "$work/usr/lib/modules/6.12.0-160100.4-default/$(dirname "$module")"
  cp -p "$module_root/$module" "$work/usr/lib/modules/6.12.0-160100.4-default/$module"
done
cp -p "$module_root"/modules.* "$work/usr/lib/modules/6.12.0-160100.4-default/"
cp -p "$(dirname "$0")/init-btrfs" "$work/init-btrfs"
chmod 0755 "$work/init-btrfs"
(cd "$work" && find . -print0 | cpio --null -o --format=newc --quiet | gzip -1 > "$output")
sha256sum "$output"
