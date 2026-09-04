#!/bin/bash
# scripts/run_p19.sh -- Phase 19 supervision & config control-surface gate.
#
# Drives the init-spawned boot shell over a serial pipe through the
# Phase 19 control plane:
#   sysctl quantum_us            (knob read, registry op 11, no cap needed)
#   sysctl quantum_ms=20         (knob write, op 12; the p19 shell holds
#                                 CAP_CONF, so this SUCCEEDS and reprograms
#                                 the scheduler quantum live)
#   top                          (SYSSTAT shows quantum=20000us)
#   caps                         (Phase 19 cap-source display: source=init)
#   checkconf                     (validators run; fresh ramdisk files are
#                                 missing, so each reports not found/skipped)
#   initctl restart fs           (init kills + respawns fs; shell prints
#                                 the new sid; run LAST — fs is rebooting)
#
# Waits for "shell ready" before typing (KVM-fast / TCG-slow safe).
set -u
LOG="$1"; shift
QEMU_BIN="$1"; shift
QEMU_ENV_STR="$1"; shift
shift # separator
D=$(mktemp -d)
mkfifo "$D/qemu.in" "$D/qemu.out"

env "$QEMU_ENV_STR" "$QEMU_BIN" "$@" \
    -serial pipe:"$D/qemu" -display none -no-reboot -net none &
QPID=$!

cat "$D/qemu.out" > "$LOG" &
CATPID=$!
exec 3>"$D/qemu.in"

READY=0
for i in $(seq 1 120); do
    sleep 2
    if grep -qa 'shell ready' "$LOG" 2>/dev/null; then
        READY=1
        break
    fi
done
if [ "$READY" != "1" ]; then
    echo "run_p19: shell never became ready" >&2
fi
sleep 3

printf 'sysctl quantum_us\r' >&3
sleep 6
printf 'sysctl quantum_ms=20\r' >&3
sleep 6
printf 'top\r' >&3
sleep 6
printf 'caps\r' >&3
sleep 6
printf 'checkconf\r' >&3
sleep 8
printf 'initctl restart fs\r' >&3
sleep 12

kill "$QPID" 2>/dev/null
wait "$QPID" 2>/dev/null
kill "$CATPID" 2>/dev/null
wait "$CATPID" 2>/dev/null
rm -rf "$D"

# Gate (asserted by make test-p19 on the stripped log):
# - sysctl read shows the 5000 default
# - sysctl write applies (quantum_us = 20000)
# - top shows the live scheduler quantum (quantum=20000us)
# - caps shows the Phase 19 source byte (source=init)
# - checkconf runs to OK (fresh ramdisk: files skipped)
# - initctl restart round-trips through init (restart fs ok)
