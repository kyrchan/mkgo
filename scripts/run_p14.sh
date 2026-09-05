#!/bin/bash
# scripts/run_p14.sh -- Phase 14 shell userland gate: pipes + built-ins.
# Feeds scripted shell commands via a serial pipe and captures output.
# Tests: cp, cat|grep|sort|head pipe chain, sleep, date.
#
# Waits for "shell ready" in the log before typing (poll with timeout),
# so the gate works under both KVM (fast boot) and TCG (slow wasm
# compile, 60s+ to first shell prompt).
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

# Wait for shell readiness (up to ~240s for TCG).
READY=0
for i in $(seq 1 120); do
    sleep 2
    if grep -qa 'shell ready' "$LOG" 2>/dev/null; then
        READY=1
        break
    fi
done
if [ "$READY" != "1" ]; then
    echo "run_p14: shell never became ready" >&2
fi
sleep 3                        # shell focus claim settles

printf 'cp /etc/motd /tmp/m\r' >&3
sleep 5
printf 'cat /tmp/m | grep kernel | sort | head -n 3\r' >&3
sleep 12
printf 'sleep 1\r' >&3
sleep 6
printf 'date\r' >&3
sleep 10

kill "$QPID" 2>/dev/null
wait "$QPID" 2>/dev/null
kill "$CATPID" 2>/dev/null
wait "$CATPID" 2>/dev/null
rm -rf "$D"

# Gate: output reaches serial
# - cp succeeded (no error from shell)
# - pipe chain produced "kernel" line
# - sleep completed (date appears after sleep)
# - date produced a date string
