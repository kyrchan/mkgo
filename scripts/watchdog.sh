#!/usr/bin/env bash
export PATH="/usr/local/bin:$PATH"
# Watchdog/supervisor for scripts/overnight.sh.
#   - restarts the runner if its process dies (max once per 5 min)
#   - unsticks hung rounds (log frozen > STALL_SECS while an opencode run
#     child is alive -> kill the child; the runner advances to next round)
#   - exits when the plan reports ALL PHASES COMPLETE, or honors .overnight-stop
#
# Launch fully detached:
#   cd /home/cyr/kernel && setsid nohup ./scripts/watchdog.sh >/dev/null 2>&1 </dev/null &
set -u
cd "$(dirname "$0")/.."

LOG=.overnight.log
STALL_SECS=${1:-900}
CHECK_INTERVAL=30
RESTART_MIN_GAP=120

last_restart=0
stale_strikes=0
while true; do
    if [ -f .overnight-stop ]; then
        sleep "$CHECK_INTERVAL"
        continue
    fi
    if [ -f .overnight-complete ]; then
        echo "[watchdog] plan complete; exiting $(date)" >>"$LOG"
        break
    fi

    if ! pgrep -f 'scripts/overnight.sh' >/dev/null 2>&1; then
        now=$(date +%s)
        if [ $((now - last_restart)) -ge "$RESTART_MIN_GAP" ]; then
            echo "[watchdog] runner dead; restarting $(date)" >>"$LOG"
            setsid nohup ./scripts/overnight.sh >/dev/null 2>&1 </dev/null &
            last_restart=$(date +%s)
        fi
    elif pgrep -f 'opencode run --auto' >/dev/null 2>&1; then
        now=$(date +%s)
        mtime=$(stat -c %Y "$LOG" 2>/dev/null || echo "$now")
        age=$((now - mtime))
        if [ "$age" -gt "$STALL_SECS" ]; then
            stale_strikes=$((stale_strikes + 1))
            # debounce: require two consecutive stale checks before killing,
            # so cold starts / provider hiccups / clock jumps survive
            if [ "$stale_strikes" -ge 2 ]; then
                echo "[watchdog] log stalled ${age}s; killing stuck round $(date)" >>"$LOG"
                pkill -f 'opencode run --auto' 2>/dev/null
                stale_strikes=0
                sleep 5
            fi
        else
            stale_strikes=0
        fi
    fi
    sleep "$CHECK_INTERVAL"
done
