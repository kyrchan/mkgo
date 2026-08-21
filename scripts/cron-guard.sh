#!/usr/bin/env bash
# Cron safety net (runs every minute): if the watchdog is gone, revive it.
# The watchdog in turn revives overnight.sh. Chain = self-healing.
cd "$(dirname "$0")/.." || exit 0
[ -f .overnight-stop ] && exit 0
if ! pgrep -f 'scripts/watchdog.sh' >/dev/null 2>&1; then
    echo "[cron-guard] watchdog missing; relaunching $(date)" >> .overnight.log
    setsid nohup ./scripts/watchdog.sh >/dev/null 2>&1 </dev/null &
fi
