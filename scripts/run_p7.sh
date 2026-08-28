#!/bin/bash
# scripts/run_p7.sh -- boot with a bidirectional serial pipe, feed a
# scripted shell command, capture output. Used by make test-p7.
#
# Phase-7 flow (current architecture): init spawns console/fs/login/shell
# from /etc/init.conf; the shell claims §4 focus when ready, so typed
# serial input goes straight to its line editor. `cat /etc/motd` proves
# input -> shell -> fs -> console-relay end to end.
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

sleep 2                        # shell focus claim settles
printf 'cat /etc/motd\r' >&3; sleep 10
sleep 3

kill "$QPID" 2>/dev/null
