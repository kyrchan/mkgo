#!/usr/bin/env bash
# Overnight autonomous runner for the kernel phase plan.
#
# Launch (survives logout):
#   cd /home/cyr/kernel && nohup ./scripts/overnight.sh >/dev/null 2>&1 & disown
#
# Early stop:   touch .overnight-stop
# Progress:     tail -f .overnight.log
#
# Uses its own dedicated session (.overnight.sid) — never attaches to an
# interactive TUI session. Model pinned in opencode.json.
set -u
cd "$(dirname "$0")/.."

LOG=.overnight.log
MAX_ROUNDS=${1:-24}

SEED="Read AGENTS.md and MEMORY.md first. You are running unattended. \
Execute the phase plan continuously and unabated per the Autonomy mandate; \
make every implementation decision yourself. Start at Phase 0 now."
CONT="Continue the phase plan from where MEMORY.md says it stands, per AGENTS.md. \
When Phases 0-5 are all green, print exactly: ALL PHASES COMPLETE"

run_round() { # $1 = prompt
    if [ -f .overnight.sid ]; then
        opencode run --auto --session "$(cat .overnight.sid)" --format json "$1"
    else
        opencode run --auto --format json "$1"
    fi
}

capture_sid() {
    local sid
    sid=$(grep -o '"sessionID"[[:space:]]*:[[:space:]]*"[^"]*"' "$LOG" \
          | tail -1 | sed 's/.*"\([^"]*\)"$/\1/')
    if [ -n "$sid" ]; then printf '%s' "$sid" >.overnight.sid; fi
}

echo "[overnight] start $(date)" >>"$LOG"
round=1
while [ "$round" -le "$MAX_ROUNDS" ]; do
    [ -f .overnight-stop ] && { echo "[overnight] stop requested $(date)" >>"$LOG"; break; }
    echo "[overnight] round $round begin $(date)" >>"$LOG"
    if [ "$round" -eq 1 ]; then
        run_round "$SEED" | tee -a "$LOG"
        capture_sid
    else
        run_round "$CONT" | tee -a "$LOG"
    fi
    echo "[overnight] round $round end $(date)" >>"$LOG"
    if tail -80 "$LOG" | grep -q "ALL PHASES COMPLETE"; then
        echo "[overnight] plan complete $(date)" >>"$LOG"
        break
    fi
    sleep 10
    round=$((round + 1))
done
echo "[overnight] exit $(date)" >>"$LOG"
