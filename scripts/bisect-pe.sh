#!/bin/bash
# Bisect which PE knob makes OVMF accept our image.
set -u
ROOT=/home/cyr/.local/osdev-root
QEMU="$ROOT/usr/bin/qemu-system-x86_64"
export LD_LIBRARY_PATH="$ROOT/usr/lib/x86_64-linux-gnu"
QARGS="-L $ROOT/usr/share/qemu -L $ROOT/usr/share/seabios -machine q35 -cpu max -m 512 -accel tcg \
 -drive if=pflash,format=raw,readonly=on,file=$ROOT/usr/share/OVMF/OVMF_CODE_4M.fd \
 -drive if=pflash,format=raw,file=build/VARS.fd -drive format=raw,file=build/disk.img \
 -display none -no-reboot -net none -serial file:build/bisect.log"

try () {
  local name="$1"; shift
  env "$@" python3 scripts/mkpefi.py build/kernel.so build/BOOTX64.EFI >/dev/null || { echo "$name: build fail"; return; }
  dd if=/dev/zero of=build/disk.img bs=1M count=0 seek=64 status=none
  "$ROOT/usr/bin/mformat" -i build/disk.img :: 2>/dev/null
  "$ROOT/usr/bin/mmd" -i build/disk.img ::/EFI ::/EFI/BOOT 2>/dev/null
  "$ROOT/usr/bin/mcopy" -i build/disk.img build/BOOTX64.EFI ::/EFI/BOOT/BOOTX64.EFI
  rm -f build/bisect.log
  timeout 45 $QEMU $QARGS >/dev/null 2>&1
  if grep -aq 'KERNEL-OK' build/bisect.log; then r=LOADED
  elif grep -aq 'failed to load Boot0001' build/bisect.log; then r=rejected
  else r='? (no boot0001 attempt)'; fi
  printf '%-28s -> %s\n' "$name" "$r"
}

try "base(fa512,reloc,tds0)"
try "fa4096"            MKPEFI_FA=4096
try "padraw"            MKPEFI_PADRAW=1
try "noreloc"           MKPEFI_NORELOC=1
try "tds-nonzero"       MKPEFI_TDS=0x60000000
try "fa4096+padraw"     MKPEFI_FA=4096 MKPEFI_PADRAW=1
try "fa4096+padraw+noreloc" MKPEFI_FA=4096 MKPEFI_PADRAW=1 MKPEFI_NORELOC=1
