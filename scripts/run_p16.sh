#!/bin/bash
# scripts/run_p16.sh -- Phase 16 net client userland gate.
#
# Boots the kernel with the net stack + shell built-ins (ping/nc/http/
# netstat/ipaddr/ssh). The shell is up, the net service is up, and the
# IP address is reported on serial. No host-side network server is needed
# for the basic bring-up gate -- the Phase 16 E2E tests live in the
# guest .wasm modules (services/net/phase16_test.go) and in the serial
# scripts that drive the shell's nc/http built-ins (test-p16sh).
set -u
LOG="$1"; shift
QEMU_BIN="$1"; shift
QEMU_ENV_STR="$1"; shift
shift # separator

env "$QEMU_ENV_STR" "$QEMU_BIN" "$@" \
    -netdev user,id=n1 -device virtio-net-pci,netdev=n1 \
    -serial file:"$LOG" -display none -no-reboot &

# gate window: firmware + bring-up + net up + shell ready
# runs in background so this script can return and make can grep the log
( sleep 60; killall -9 qemu-system-x86_64 2>/dev/null ) &
exit 0
