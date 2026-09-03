#!/bin/bash
# scripts/run_p18.sh -- Phase 18 package management gate.
#
# Boots the kernel with the Phase 18 pkg built-in (list/install/remove).
# The shell's pkg list shows installed modules from /boot/modules.
set -u
LOG="$1"; shift
QEMU_BIN="$1"; shift
QEMU_ENV_STR="$1"; shift
shift # separator

env "$QEMU_ENV_STR" "$QEMU_BIN" "$@" \
    -netdev user,id=n1 -device virtio-net-pci,netdev=n1 \
    -serial file:"$LOG" -display none -no-reboot &

# gate window: firmware + bring-up + shell ready
( sleep 60; killall -9 qemu-system-x86_64 2>/dev/null ) &
exit 0
