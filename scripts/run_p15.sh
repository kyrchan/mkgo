#!/bin/bash
# scripts/run_p15.sh -- Phase 15 identity/auth/observability gate.
# Drives the init-spawned boot shell (uid 0, FOCUS-only caps) over a
# serial pipe: top, a capability-denied kill (audit trail), dmesg/audit
# readback of that denial, passwd change, memstat.
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
    echo "run_p15: shell never became ready" >&2
fi
sleep 3

printf 'top\r' >&3
sleep 6
printf 'kill-session 1\r' >&3
sleep 6
printf 'dmesg\r' >&3
sleep 8
printf 'audit KILL\r' >&3
sleep 6
printf 'passwd newpass15\r' >&3
sleep 6
printf 'memstat\r' >&3
sleep 8

kill "$QPID" 2>/dev/null
wait "$QPID" 2>/dev/null
kill "$CATPID" 2>/dev/null
wait "$CATPID" 2>/dev/null
rm -rf "$D"

# Gate (asserted by make test-p15 on the stripped log):
# - top shows live sessions
# - kill-session denied (shell lacks CAP_KILL)
# - dmesg/audit show the recorded KILL cap denial
# - passwd change accepted
# - memstat pool dump present
