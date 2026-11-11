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

# --- setup_rootless_docker ---------------------------------------------------------
type setup_rootless_docker >/dev/null 2>&1 || { bad "setup_rootless_docker not defined"; exit 1; }
for c in apt-get loginctl runuser apparmor_parser systemctl; do
  cat >"$bin/$c" <<'S'
#!/bin/sh
echo "$(basename "$0") $*" >>"$STUB_LOG"
case "$(basename "$0") $*" in *"$STUB_FAIL_MATCH"*) [ -n "${STUB_FAIL_MATCH:-}" ] && exit 1 ;; esac
exit 0
S
  chmod +x "$bin/$c"
done
export STUB_LOG="$tmp/rd.log"
etc="$tmp/etc"; runroot="$tmp/run"; mkdir -p "$etc" "$runroot/1001"
python3 -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])' "$runroot/1001/bus"
export JAUNT_RUN_USER_DIR="$runroot"
: >"$STUB_LOG"
setup_rootless_docker alice 1001 "$etc" >/dev/null 2>&1 || bad "setup_rootless_docker returned non-zero"
setup_rootless_docker alice 1001 "$etc" >/dev/null 2>&1 || bad "second run returned non-zero"
[ "$(grep -c '^alice:' "$etc/subuid")" = 1 ] || bad "subuid not added exactly once: $(cat "$etc/subuid")"
[ "$(grep -c '^alice:' "$etc/subgid")" = 1 ] || bad "subgid not added exactly once"
[ "$(grep '^alice:' "$etc/subuid")" = "alice:100000:65536" ] || bad "subuid line wrong"
[ "$(grep '^alice:' "$etc/subgid")" = "alice:100000:65536" ] || bad "subgid line wrong"
ru='runuser -u alice -- env XDG_RUNTIME_DIR=/run/user/1001 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus'
want_prof='abi <abi/4.0>,
include <tunables/global>

profile rootlesskit /usr/bin/rootlesskit flags=(unconfined) {
  userns,
  include if exists <local/usr.bin.rootlesskit>
}'
[ "$(cat "$etc/apparmor.d/usr.bin.rootlesskit")" = "$want_prof" ] || bad "apparmor profile mismatch"
grep -q "^apparmor_parser -r $etc/apparmor.d/usr.bin.rootlesskit\$" "$STUB_LOG" || bad "apparmor_parser -r not called: $(cat "$STUB_LOG")"
grep -q '^apt-get install .*uidmap.*dbus-user-session.*slirp4netns' "$STUB_LOG" || bad "base packages not installed"
grep -q 'docker-ce-rootless-extras' "$STUB_LOG" || bad "rootless extras not installed"
grep -q '^loginctl enable-linger alice$' "$STUB_LOG" || bad "enable-linger not called"
grep -q '^systemctl start user@1001.service$' "$STUB_LOG" || bad "user@1001.service not started"
[ "$(grep -n -e '^loginctl enable-linger' -e '^systemctl start user@' -e 'dockerd-rootless-setuptool' "$STUB_LOG" | head -3 | cut -d: -f2- | cut -d' ' -f1-2 | tr '\n' '|')" = "loginctl enable-linger|systemctl start|runuser -u|" ] || bad "wrong step order: $(cat "$STUB_LOG")"
grep -q "^$ru docker context create rootless --docker host=unix:///run/user/1001/docker.sock\$" "$STUB_LOG" || bad "context create not run"
grep -q "^$ru dockerd-rootless-setuptool.sh install\$" "$STUB_LOG" || bad "setuptool not run as the user: $(cat "$STUB_LOG")"
grep -q "^$ru systemctl --user enable --now docker\$" "$STUB_LOG" || bad "systemctl --user enable not run"
grep -q "^$ru docker context use rootless\$" "$STUB_LOG" || bad "docker context use rootless not run"

# A failing setuptool warns and still returns 0 under set -e.
: >"$STUB_LOG"
out=$(set -e; STUB_FAIL_MATCH=dockerd-rootless-setuptool setup_rootless_docker alice 1001 "$etc" 2>&1; echo reached)
case "$out" in *"WARNING: rootless docker setup failed: "*) ;; *) bad "no rootless warning: $out" ;; esac
case "$out" in *reached) ;; *) bad "failing setuptool aborted under set -e: $out" ;; esac
grep -q 'systemctl --user' "$STUB_LOG" && bad "continued past the failed setuptool"

# Reused /home: the unit exists, so install is skipped but enable and context still run.
h="$tmp/home-alice"; mkdir -p "$h/.config/systemd/user"; : >"$h/.config/systemd/user/docker.service"
: >"$STUB_LOG"
setup_rootless_docker alice 1001 "$etc" "$h" >/dev/null 2>&1 || bad "reused-home run returned non-zero"
grep -q 'dockerd-rootless-setuptool' "$STUB_LOG" && bad "setuptool ran although the unit exists"
grep -q "^$ru systemctl --user enable --now docker\$" "$STUB_LOG" || bad "reused home: enable not run"
grep -q "^$ru docker context use rootless\$" "$STUB_LOG" || bad "reused home: context use not run"

# Static checks on the script text.

grep -q 'usermod -aG docker' "$here/dev-setup.sh" && bad "dev-setup.sh still adds the user to docker"
grep -q 'NOPASSWD:ALL' "$here/dev-setup.sh" && bad "dev-setup.sh still has NOPASSWD:ALL"
grep -q 'gpasswd -d "\$JAUNT_USER" docker' "$here/dev-setup.sh" || bad "dev-setup.sh does not remove the user from docker"

[ "$fail" = 0 ] && echo "PASS dev-setup_test.sh"
exit "$fail"
