#!/usr/bin/env bash
# Parameterized single-lane runner for the parallel fleet.
# Env (all relative paths resolve inside LANE_DIR):
#   LANE_DIR        worktree root; agent's cwd            [required]
#   LANE_NAME       tag used in log markers               [required]
#   LANE_LOG        log path      [default $LANE_DIR/.lane.log]
#   LANE_SID        session file  [default $LANE_DIR/.lane.sid]
#   LANE_SEED_FILE  first-round prompt text file          [required]
#   LANE_CONT_FILE  continuation prompt text file         [required]
#   LANE_MAX_ROUNDS rounds cap    [default 200]
#
# Supervision contract (used by scripts/fleet.sh):
#   writes .runner.pid at start, .opencode.pid while a round's child runs,
#   .overnight-complete when the assistant emits the exact sentinel.
set -u
: "${LANE_DIR:?LANE_DIR required}" "${LANE_NAME:?LANE_NAME required}"
LANE_LOG=${LANE_LOG:-$LANE_DIR/.lane.log}
LANE_SID=${LANE_SID:-$LANE_DIR/.lane.sid}
LANE_MAX_ROUNDS=${LANE_MAX_ROUNDS:-200}
cd "$LANE_DIR" || exit 1

echo $$ >"$LANE_DIR/.runner.pid"
SENTINEL="ALL PHASES COMPLETE"
SEED=$(cat "${LANE_SEED_FILE:?}") || exit 1
CONT=$(cat "${LANE_CONT_FILE:?}") || exit 1

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

run_round() { # $1 = prompt ; records child pid for precise supervision
    if [ -f "$LANE_SID" ]; then
        opencode run --auto --session "$(cat "$LANE_SID")" --format json "$1" &
    else
        opencode run --auto --format json "$1" &
    fi
    local cp=$!
    echo "$cp" >"$LANE_DIR/.opencode.pid"
    wait "$cp"
    local rc=$?
    rm -f "$LANE_DIR/.opencode.pid"
    return $rc
}

capture_sid() {
    local sid
    sid=$(grep -o '"sessionID"[[:space:]]*:[[:space:]]*"[^"]*"' "$LANE_LOG" \
          | tail -1 | sed 's/.*"\([^"]*\)"$/\1/')
    [ -n "$sid" ] && printf '%s' "$sid" >"$LANE_SID"
}

echo "[$LANE_NAME] start $(date)" >>"$LANE_LOG"
round=1
while [ "$round" -le "$LANE_MAX_ROUNDS" ]; do
    [ -f .overnight-stop ] && { echo "[$LANE_NAME] stop $(date)" >>"$LANE_LOG"; break; }
    [ -f .overnight-complete ] && break
    CHUNK=$(mktemp)
    echo "[$LANE_NAME] round $round begin $(date)" >>"$LANE_LOG"
    if [ "$round" -eq 1 ]; then
        run_round "$SEED" | tee -a "$LANE_LOG" | tee "$CHUNK" >/dev/null
        capture_sid
    else
        run_round "$CONT" | tee -a "$LANE_LOG" | tee "$CHUNK" >/dev/null
    fi
    echo "[$LANE_NAME] round $round end $(date)" >>"$LANE_LOG"
    if sentinel_in_chunk "$CHUNK"; then
        touch .overnight-complete
        echo "[$LANE_NAME] sentinel emitted $(date)" >>"$LANE_LOG"
        rm -f "$CHUNK"; break
    fi
    rm -f "$CHUNK"
    sleep 10
    round=$((round + 1))
done
rm -f "$LANE_DIR/.runner.pid"
echo "[$LANE_NAME] exit $(date)" >>"$LANE_LOG"
