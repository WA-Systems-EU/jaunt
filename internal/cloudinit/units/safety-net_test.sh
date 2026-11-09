#!/bin/sh
# Tests decide_safety. Run: sh internal/cloudinit/units/safety-net_test.sh
here=$(cd "$(dirname "$0")" && pwd)
JAUNTBOX_TEST=1
export JAUNTBOX_TEST
. "$here/safety-net.sh"
fail=0
check() {
  got=$(decide_safety "$1" "$2")
  if [ "$got" != "$3" ]; then
    echo "FAIL decide_safety $1 $2: got $got want $3"
    fail=1
  fi
}
check 600 -1 KEEP
check 2701 -1 DELETE
check 2701 100 KEEP
check 2701 901 DELETE
check 99999 30 KEEP
[ "$fail" = 0 ] && echo "ok"
exit "$fail"
