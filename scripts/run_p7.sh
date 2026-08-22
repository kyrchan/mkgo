#!/bin/bash
# scripts/run_p7.sh -- boot with a bidirectional serial pipe, feed a
# scripted login + command, capture output. Used by make test-p7.
set -u
LOG="$1"; shift
QEMU_BIN="$1"; shift
QEMU_ENV_STR="$1"; shift
shift # separator
# remaining args: QEMU machine args (no -serial)
D=$(mktemp -d)
mkfifo "$D/qemu.in" "$D/qemu.out"

env "$QEMU_ENV_STR" "$QEMU_BIN" "$@" \
    -serial pipe:"$D/qemu" -display none -no-reboot -net none &
QPID=$!

# hold the output open and tee to the log
cat "$D/qemu.out" > "$LOG" &
CATPID=$!
exec 3>"$D/qemu.in"

sleep 22                       # firmware boot + init spawns services

printf 'u1\r' >&3; sleep 4     # login: user u1
printf 'u1\r' >&3; sleep 7     # password -> auth + focus moves to shell
sleep 2
printf 'cat /etc/motd\r' >&3; sleep 10
sleep 3

kill "$QPID" 2>/dev/null
wait "$QPID" 2>/dev/null
kill "$CATPID" 2>/dev/null
rm -rf "$D"
exit 0
