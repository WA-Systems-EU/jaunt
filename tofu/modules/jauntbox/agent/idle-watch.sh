#!/bin/sh
# jaunt idle-watch agent. Provider-neutral.
# decide() is pure: no clock, no /proc, no files — all inputs are arguments.

decide() {
  as=$1; cpu=$2; cpu_t=$3; keep=$4; now=$5; idle=$6; night=$7; day_w=$8; night_w=$9
  shift 9; grace=$1

  [ "$as" -gt 0 ] && { echo KEEP; return; }          # rule 1: active session vetoes, always
  [ "$keep" -gt "$now" ] && { echo KEEP; return; }   # rule 2: keep marker in the future
  [ "$cpu" -ge "$cpu_t" ] && { echo KEEP; return; }  # busy (running build) is not idle

  if [ "$night" -eq 1 ]; then w=$night_w; else w=$day_w; fi
  if [ "$idle" -le "$w" ]; then echo KEEP
  elif [ "$idle" -le $((w + grace)) ]; then echo WARN
  else echo DELETE
  fi
}

# --- real-input gathering + action (skipped under test) ---
main() {
  now=$(date +%s)
  hour=$(date +%H)
  if [ "$hour" -ge 22 ] || [ "$hour" -lt 7 ]; then is_night=1; else is_night=0; fi

  active=$(who | wc -l)
  tmux ls >/dev/null 2>&1 && active=$((active + 1))
  cpu=$(awk '{printf "%d", $1*100/'"$(nproc)"'}' /proc/loadavg)   # rough % of one core-equiv

  keep_file=/var/lib/jaunt/keep-until
  keep=0; [ -f "$keep_file" ] && keep=$(cat "$keep_file")

  act_file=/var/lib/jaunt/last-active
  [ -f "$act_file" ] || echo "$now" > "$act_file"
  last=$(cat "$act_file")
  if [ "$active" -gt 0 ] || [ "$cpu" -ge "${JAUNT_CPU_THRESHOLD:-15}" ]; then
    echo "$now" > "$act_file"; last=$now
  fi
  idle=$((now - last))

  verdict=$(decide "$active" "$cpu" "${JAUNT_CPU_THRESHOLD:-15}" "$keep" "$now" \
                   "$idle" "$is_night" "${JAUNT_IDLE_DAY:-7200}" \
                   "${JAUNT_IDLE_NIGHT:-3600}" "${JAUNT_GRACE:-120}")
  case "$verdict" in
    WARN)   logger -t jaunt "idle warning: box will self-delete soon"; \
            [ -x /opt/jaunt/notify.sh ] && /opt/jaunt/notify.sh "jaunt: idle, deleting soon" ;;
    DELETE) logger -t jaunt "idle: self-deleting"; \
            [ -x /opt/jaunt/self-delete.sh ] && /opt/jaunt/self-delete.sh ;;
    *)      : ;;
  esac
}

[ "${JAUNT_TEST:-0}" = "1" ] || main "$@"
