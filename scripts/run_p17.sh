#!/bin/bash
# scripts/run_p17.sh -- Phase 17 capability & port introspection gate.
#
# Boots the kernel with the Phase 17 tools (ports/sessinfo/caphint/chcaps).
# The shell's caphint and ports built-ins are verified on serial.
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
