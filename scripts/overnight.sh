#!/usr/bin/env bash
export PATH="/usr/local/bin:$PATH"
set -u
cd "$(dirname "$0")/.."

LOG=.overnight.log
MAX_ROUNDS=${1:-200}
ROTATE_EVERY=${LANE_ROTATE_EVERY:-12}

SEED="Read AGENTS.md and MEMORY.md first. You are running unattended. \
Execute the phase plan continuously and unabated per the Autonomy mandate; \
make every implementation decision yourself. Never end your turn while \
productive work remains: if blocked more than ~10 minutes on one obstacle, \
switch to another track (services/, guests/, docs) and return later. \
Start at Phase 0 now."
CONT="Continue the phase plan from where MEMORY.md says it stands, per AGENTS.md. \
Never stop while work remains; switch tracks when blocked. \
Completion is decided by the COORDINATOR, not by you: keep working until stopped."

PROMPT_OVERRIDE=/home/cyr/kernel/.mainline-prompt
if [ -f "$PROMPT_OVERRIDE" ]; then
    SEED=$(cat "$PROMPT_OVERRIDE"); CONT=$(cat "$PROMPT_OVERRIDE")
    echo "[overnight] MISSION OVERRIDE active ($PROMPT_OVERRIDE)">>"$LOG"
fi

run_round() { # $1 = prompt ; streams JSON events to stdout
    if [ -f .overnight.sid ]; then
        kilo run --auto --session "$(cat .overnight.sid)" --format json "$1" 2>&1
    else
        kilo run --auto --format json "$1" 2>&1
    fi
}

capture_sid() {
    local sid
    sid=$(grep -o '"sessionID"[[:space:]]*:[[:space:]]*"[^"]*"' "$LOG" \
          | tail -1 | sed 's/.*"\([^"]*\)"//')
    if [ -n "$sid" ]; then printf '%s' "$sid" >.overnight.sid; fi
}

echo "[overnight] start $(date)">>"$LOG"
round=1
while [ "$round" -le "$MAX_ROUNDS" ]; do
    [ -f .overnight-stop ] && { echo "[overnight] stop requested $(date)">>"$LOG"; break; }
    CHUNK=$(mktemp)
    echo "[overnight] round $round begin $(date)">>"$LOG"
    if [ "$round" -eq 1 ]; then
        run_round "$SEED" | tee -a "$LOG" | tee "$CHUNK" >/dev/null
        capture_sid
    else
        run_round "$CONT" | tee -a "$LOG" | tee "$CHUNK" >/dev/null
    fi
    echo "[overnight] round $round end $(date)">>"$LOG"
    rm -f "$CHUNK"
    if [ $((round % ROTATE_EVERY)) -eq 0 ] && [ -f .overnight.sid ]; then
        echo "[overnight] rotating session (context hygiene) $(date)">>"$LOG"
        rm -f .overnight.sid
    fi
    sleep 10
    round=$((round + 1))
done
echo "[overnight] exit $(date)">>"$LOG"
