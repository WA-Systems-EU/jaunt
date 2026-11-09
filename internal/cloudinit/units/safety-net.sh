#!/bin/sh
# Heartbeat safety net: if the jauntbox supervisor has stopped touching the
# heartbeat file, delete this server so a broken supervisor cannot leak a box.
HEARTBEAT=/var/lib/jauntbox/heartbeat
TOKEN_FILE=/etc/jauntbox/hcloud.token

# decide_safety UPTIME_SECS HEARTBEAT_AGE_SECS  (age is -1 when the file is missing)
decide_safety() {
  if [ "$1" -gt 2700 ] && { [ "$2" -eq -1 ] || [ "$2" -gt 900 ]; }; then
    echo DELETE
  else
    echo KEEP
  fi
}

main() {
  set -eu
  uptime_secs=$(cut -d. -f1 /proc/uptime)
  if [ -e "$HEARTBEAT" ]; then
    age=$(( $(date +%s) - $(stat -c %Y "$HEARTBEAT") ))
  else
    age=-1
  fi
  [ "$(decide_safety "$uptime_secs" "$age")" = DELETE ] || exit 0
  token=$(cat "$TOKEN_FILE")
  id=$(curl -fsSL --max-time 30 --retry 3 http://169.254.169.254/hetzner/v1/metadata/instance-id)
  # The token goes in through stdin (-H @-) so it never shows up in ps.
  printf 'Authorization: Bearer %s\n' "$token" | curl -fsSL --max-time 30 --retry 3 -X DELETE -H @- \
    "https://api.hetzner.cloud/v1/servers/$id"
}

if [ "${JAUNTBOX_TEST:-}" != 1 ]; then
  main "$@"
fi
