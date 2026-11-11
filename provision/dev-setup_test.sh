#!/usr/bin/env bash
# Tests the sudoers writer in dev-setup.sh with a stub visudo. Run: bash provision/dev-setup_test.sh
set -u
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
bad() { echo "FAIL $*"; fail=1; }

bin="$tmp/bin"; mkdir -p "$bin"
cat >"$bin/visudo" <<'S'
#!/bin/sh
# stub: visudo -cf <file>
[ -n "${STUB_VISUDO_FAIL:-}" ] && { echo "visudo: parse error" >&2; exit 1; }
[ "$1" = "-cf" ] && [ -f "$2" ] || exit 2
exit 0
S
chmod +x "$bin/visudo"
PATH="$bin:$PATH"

# Sourcing must define functions without running provisioning.
# shellcheck disable=SC1091
source "$here/dev-setup.sh" || { bad "sourcing dev-setup.sh failed"; exit 1; }
type write_sudoers >/dev/null 2>&1 || { bad "write_sudoers not defined"; exit 1; }

dir="$tmp/sudoers.d"; mkdir -p "$dir"
want='alice ALL=(ALL:ALL) ALL
alice ALL=(root) NOPASSWD: /usr/local/bin/jauntctl mint *, /usr/local/bin/jauntctl repos, /usr/local/bin/jauntctl repos *, /usr/local/bin/jauntctl box-status, /usr/local/bin/jauntctl box-status *, /usr/local/bin/jauntctl agent *'

write_sudoers alice "$dir" >/dev/null 2>&1 || bad "write_sudoers returned non-zero"
f="$dir/90-jaunt-alice"
[ "$(cat "$f")" = "$want" ] || bad "content mismatch: $(cat "$f" 2>&1)"
[ "$(stat -c %a "$f")" = "440" ] || bad "mode is $(stat -c %a "$f"), want 440"
[ "$(ls "$dir" | wc -l)" = 1 ] || bad "stray files in $dir: $(ls "$dir")"

# A failing visudo keeps the previous file, leaves no temp, warns.
rm -f "$f"; echo "previous" >"$f"
out=$(STUB_VISUDO_FAIL=1 write_sudoers alice "$dir" 2>&1)
[ "$(cat "$f")" = "previous" ] || bad "failing visudo replaced the file"
[ "$(ls "$dir" | wc -l)" = 1 ] || bad "temp file left behind: $(ls -A "$dir")"
case "$out" in *WARNING*) ;; *) bad "no warning logged: $out" ;; esac

# Static checks on the script text.
grep -q 'usermod -aG docker' "$here/dev-setup.sh" && bad "dev-setup.sh still adds the user to docker"
grep -q 'NOPASSWD:ALL' "$here/dev-setup.sh" && bad "dev-setup.sh still has NOPASSWD:ALL"
grep -q 'gpasswd -d "\$JAUNT_USER" docker' "$here/dev-setup.sh" || bad "dev-setup.sh does not remove the user from docker"

[ "$fail" = 0 ] && echo "PASS dev-setup_test.sh"
exit "$fail"
