#!/bin/sh
# Pure table tests for decide(). No external deps.
set -eu
DIR=$(dirname "$0")
JAUNT_TEST=1 . "$DIR/idle-watch.sh"   # JAUNT_TEST guard prevents main() from running

fail=0
check() {  # check <description> <expected> <actual>
  if [ "$2" = "$3" ]; then
    echo "ok   - $1"
  else
    echo "FAIL - $1: expected $2 got $3"
    fail=1
  fi
}

DAY=7200      # 2h
NIGHT=3600    # 1h
GRACE=120     # 2m
CPU_T=15      # busy threshold %

# 1. Active session always KEEPs — even deep in the night, long idle timer, no keep marker.
check "active session vetoes at night" KEEP \
  "$(decide 1 0 $CPU_T 0 1000000 999999 1 $DAY $NIGHT $GRACE)"
# 2. keep_marker in the future KEEPs.
check "future keep marker" KEEP \
  "$(decide 0 0 $CPU_T 2000 1000 999999 0 $DAY $NIGHT $GRACE)"
# 3. CPU above threshold = busy = KEEP (a running build).
check "busy cpu keeps" KEEP \
  "$(decide 0 90 $CPU_T 0 1000 999999 0 $DAY $NIGHT $GRACE)"
# 4. Idle but within day window: KEEP.
check "idle under day window" KEEP \
  "$(decide 0 0 $CPU_T 0 1000 7000 0 $DAY $NIGHT $GRACE)"
# 5. Idle just past day window, within grace: WARN.
check "past day window in grace warns" WARN \
  "$(decide 0 0 $CPU_T 0 1000 7250 0 $DAY $NIGHT $GRACE)"
# 6. Idle past day window + grace: DELETE.
check "past day window and grace deletes" DELETE \
  "$(decide 0 0 $CPU_T 0 1000 7400 0 $DAY $NIGHT $GRACE)"
# 7. Night window is tighter: idle 3700s at night is past 1h window → WARN.
check "night tighter window warns earlier" WARN \
  "$(decide 0 0 $CPU_T 0 1000 3700 1 $DAY $NIGHT $GRACE)"
# 8. Same 3700s idle in the day is under 2h → KEEP.
check "day window lenient for same idle" KEEP \
  "$(decide 0 0 $CPU_T 0 1000 3700 0 $DAY $NIGHT $GRACE)"
# 9. CPU boundary: exactly at threshold counts as busy → KEEP.
check "cpu at threshold is busy" KEEP \
  "$(decide 0 15 $CPU_T 0 1000 999999 0 $DAY $NIGHT $GRACE)"
# 10. Expired keep marker does not save an idle box.
check "expired keep marker deletes" DELETE \
  "$(decide 0 0 $CPU_T 500 1000 7400 0 $DAY $NIGHT $GRACE)"

exit $fail
