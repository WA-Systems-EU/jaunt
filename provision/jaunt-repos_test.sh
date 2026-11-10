#!/usr/bin/env bash
# Tests jaunt-repos.sh with stubbed sudo/jauntctl/git/gh. Run: bash provision/jaunt-repos_test.sh
set -u
here=$(cd "$(dirname "$0")" && pwd)
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is required to run this test"; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
bad() { echo "FAIL $*"; fail=1; }

bin="$tmp/bin"; mkdir -p "$bin"
cat >"$bin/sudo" <<'S'
#!/bin/sh
[ "$1" = "-n" ] && shift
exec "$@"
S
cat >"$bin/jauntctl" <<'S'
#!/bin/sh
if [ -n "${STUB_JAUNTCTL_FAIL:-}" ]; then echo "$STUB_JAUNTCTL_FAIL" >&2; exit 1; fi
[ "$1 $2" = "repos --json" ] || { echo "unexpected args: $*" >&2; exit 2; }
cat <<J
[{"owner":"acme","name":"alpha","default_branch":"main","clone_url":"https://github.com/acme/alpha.git"},
 {"owner":"acme","name":"beta","default_branch":"trunk","clone_url":"https://github.com/acme/beta.git"}]
J
S
cat >"$bin/git" <<'S'
#!/bin/sh
echo "git $*" >>"$STUB_LOG"
exit 0
S
cat >"$bin/gh" <<'S'
#!/bin/sh
echo "gh $*" >>"$STUB_GH_LOG"
exit 1
S
chmod +x "$bin"/*

run() { # name stdin -> sets out, err, rc; fresh HOME per case
  home="$tmp/$1"; mkdir -p "$home"
  export HOME="$home" JAUNT_SRC="$home/src" STUB_LOG="$home/git.log" STUB_GH_LOG="$home/gh.log"
  : >"$STUB_LOG"
  out=$(printf '%s\n' "$2" | PATH="$bin:$PATH" bash "$here/jaunt-repos.sh" 2>"$home/err"); rc=$?
  err=$(cat "$home/err")
}

# 1. all -> two clones, default branches shown, no gh
run all all
[ "$rc" = 0 ] || bad "all: rc=$rc err=$err"
grep -qx "git clone https://github.com/acme/alpha.git $JAUNT_SRC/alpha" "$STUB_LOG" || bad "all: alpha clone missing"
grep -qx "git clone https://github.com/acme/beta.git $JAUNT_SRC/beta" "$STUB_LOG" || bad "all: beta clone missing"
echo "$out" | grep -q "trunk" || bad "all: default branch not shown"
[ ! -e "$STUB_GH_LOG" ] || bad "all: gh was invoked"
echo "$err" | grep -q "gh auth login" && bad "all: unexpected warning without hosts.yml"

# 2. hosts.yml -> warning on stderr
mkdir -p "$tmp/warn/.config/gh"; : >"$tmp/warn/.config/gh/hosts.yml"
run warn ""
echo "$err" | grep -qF 'warning: an account-wide GitHub login (gh auth login) is stored on this volume; run "gh auth logout" and revoke it at https://github.com/settings/applications' || bad "warn: warning missing on stderr"
[ ! -s "$STUB_LOG" ] || bad "warn: blank pick cloned something"

# 3. existing dir is skipped and not listed as a candidate
mkdir -p "$tmp/skip/src/alpha/.git"
run skip all
grep -q "alpha.git" "$STUB_LOG" && bad "skip: alpha re-cloned"
grep -qx "git clone https://github.com/acme/beta.git $JAUNT_SRC/beta" "$STUB_LOG" || bad "skip: beta not cloned"

# 4. numbered pick
run num 1
[ "$(wc -l <"$STUB_LOG")" = 1 ] && grep -q "alpha.git" "$STUB_LOG" || bad "num: expected only alpha"

# 5. jauntctl failure -> exit 1 with message
msg='the box is locked: its forge and agent credentials are not in memory (it was rebooted or never unlocked); run `jauntbox unlock` on your laptop'
STUB_JAUNTCTL_FAIL="$msg" run locked all
[ "$rc" = 1 ] || bad "locked: rc=$rc want 1"
echo "$err$out" | grep -qF "$msg" || bad "locked: message not shown"
[ ! -s "$STUB_LOG" ] || bad "locked: cloned something"

[ "$fail" = 0 ] && echo "ok"
exit "$fail"
