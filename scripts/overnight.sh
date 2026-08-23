#!/usr/bin/env bash
# Overnight autonomous runner for the kernel phase plan.
#
# Launch fully detached:
#   cd /home/cyr/kernel && setsid nohup ./scripts/overnight.sh >/dev/null 2>&1 </dev/null &
#
# Early stop:   touch .overnight-stop     Completion: .overnight-complete
# Progress:     tail -f .overnight.log
#
# Owns a dedicated session (.overnight.sid). Completion is signaled ONLY
# by .overnight-complete, created when the ASSISTANT's own emitted text
# equals the sentinel exactly — never by grepping the raw log (prompts
# contain the phrase too; that caused a false-positive livelock once).
set -u
cd "$(dirname "$0")/.."

LOG=.overnight.log
MAX_ROUNDS=${1:-200}

SEED="Read AGENTS.md and MEMORY.md first. You are running unattended. \
Execute the phase plan continuously and unabated per the Autonomy mandate; \
make every implementation decision yourself. Never end your turn while \
productive work remains: if blocked more than ~10 minutes on one obstacle, \
switch to another track (services/, guests/, docs) and return later. \
Start at Phase 0 now."
CONT="Continue the phase plan from where MEMORY.md says it stands, per AGENTS.md. \
Never stop while work remains; switch tracks when blocked. \
When every phase gate in AGENTS.md is green, your FINAL line must be exactly: ALL PHASES COMPLETE"

sentinel_in_chunk() { # $1 = chunk file ; true iff a text event's LAST
    python3 - "$1" <<'PYEOF'      # non-empty line is exactly the sentinel
import json, sys
try:
    for line in open(sys.argv[1], encoding='utf-8', errors='ignore'):
        try:
            e = json.loads(line)
        except Exception:
            continue
        if e.get('type') != 'text':
            continue
        t = e.get('part', {}).get('text')
        if not isinstance(t, str):
            continue
        lines = [l.strip() for l in t.strip().splitlines() if l.strip()]
        if lines and lines[-1] == 'ALL PHASES COMPLETE':
            sys.exit(0)
except Exception:
    pass
sys.exit(1)
PYEOF
}

run_round() { # $1 = prompt ; streams JSON events to stdout
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
    [ -f .overnight-complete ] && break
    CHUNK=$(mktemp)
    echo "[overnight] round $round begin $(date)" >>"$LOG"
    if [ "$round" -eq 1 ]; then
        run_round "$SEED" | tee -a "$LOG" | tee "$CHUNK" >/dev/null
        capture_sid
    else
        run_round "$CONT" | tee -a "$LOG" | tee "$CHUNK" >/dev/null
    fi
    echo "[overnight] round $round end $(date)" >>"$LOG"
    if sentinel_in_chunk "$CHUNK"; then
        touch .overnight-complete
        echo "[overnight] sentinel emitted; plan complete $(date)" >>"$LOG"
        rm -f "$CHUNK"
        break
    fi
    rm -f "$CHUNK"
    sleep 10
    round=$((round + 1))
done
echo "[overnight] exit $(date)" >>"$LOG"
