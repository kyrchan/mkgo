#!/usr/bin/env bash
export PATH="/usr/local/bin:$PATH"
# Cron safety net (every minute): revive fleet supervisor and the MAINLINE
# watchdog if either is gone. They in turn revive their runners.
cd "$(dirname "$0")/.." || exit 0
[ -f .overnight-stop ] && exit 0
if ! pgrep -f 'scripts/watchdog.sh' >/dev/null 2>&1; then
    echo "[cron-guard] watchdog missing; relaunching $(date)" >>.overnight.log
    setsid nohup ./scripts/watchdog.sh >/dev/null 2>&1 </dev/null &
fi
if ! pgrep -f 'scripts/fleet.sh' >/dev/null 2>&1; then
    echo "[cron-guard] fleet missing; relaunching $(date)" >>.overnight.log
    setsid nohup ./scripts/fleet.sh >/dev/null 2>&1 </dev/null &
fi
