#!/usr/bin/env bash
export PATH="/usr/local/bin:$PATH"
# Fleet supervisor: watches every lane declared in scripts/lanes.conf
# (format: name|dir|log|sidfile|seedfile|contfile), revives dead runners,
# unsticks hung rounds by killing exactly that lane's recorded child PID.
# Complements scripts/watchdog.sh which still owns lane MAINLINE.
#
# Launch fully detached:
#   cd /home/cyr/kernel && setsid nohup ./scripts/fleet.sh >/dev/null 2>&1 </dev/null &
set -u
cd "$(dirname "$0")" || exit 1
CONF=lanes.conf
STALL_SECS=${STALL_SECS:-900}
INTERVAL=${INTERVAL:-30}
MIN_GAP=${MIN_GAP:-120}

declare -A LAST_RESTART STRIKES

line_ok() { [ -n "$1" ] && [ "${1#\#}" = "$1" ]; }

while true; do
    if [ -f ../kernel/.overnight-stop ] || [ -f /home/cyr/kernel/.overnight-stop ]; then
        sleep "$INTERVAL"; continue
    fi
    while IFS='|' read -r name dir log sid seed cont; do
        line_ok "$name" || continue
        done_marker="$dir/.overnight-complete"
        stopper="$dir/.overnight-stop"
        if [ -f "$done_marker" ]; then continue; fi

        # runner alive?
        runner_alive=0
        if [ -f "$dir/.runner.pid" ]; then
            rp=$(cat "$dir/.runner.pid" 2>/dev/null)
            [ -n "$rp" ] && kill -0 "$rp" 2>/dev/null && runner_alive=1
        fi

        if [ "$runner_alive" -eq 0 ]; then
            now=$(date +%s)
            last=${LAST_RESTART[$name]:-0}
            if [ $((now - last)) -ge "$MIN_GAP" ]; then
                echo "[fleet:$name] runner dead; relaunching $(date)" >>"$log"
                rm -f "$dir/.opencode.pid"
                LANE_DIR="$dir" LANE_NAME="$name" LANE_LOG="$log" \
                    LANE_SID="$sid" LANE_SEED_FILE="$seed" LANE_CONT_FILE="$cont" \
                    setsid nohup ./lane.sh >/dev/null 2>&1 </dev/null &
                LAST_RESTART[$name]=$now
                STRIKES[$name]=0
            fi
        else
            # round in flight? stall check on this lane's log
            if [ -f "$dir/.opencode.pid" ]; then
                now=$(date +%s)
                mtime=$(stat -c %Y "$log" 2>/dev/null || echo "$now")
                age=$((now - mtime))
                if [ "$age" -gt "$STALL_SECS" ]; then
                    strikes=$(( ${STRIKES[$name]:-0} + 1 ))
                    STRIKES[$name]=$strikes
                    if [ "$strikes" -ge 2 ]; then
                        cp=$(cat "$dir/.opencode.pid" 2>/dev/null)
                        echo "[fleet:$name] stalled ${age}s; killing child $cp $(date)" >>"$log"
                        [ -n "$cp" ] && kill "$cp" 2>/dev/null
                        STRIKES[$name]=0
                        sleep 5
                    fi
                else
                    STRIKES[$name]=0
                fi
            else
                STRIKES[$name]=0
            fi
        fi
    done <"$CONF"
    sleep "$INTERVAL"
done
