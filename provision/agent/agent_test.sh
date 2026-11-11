#!/usr/bin/env bash
# Tests the in-container agent scripts against a local bare repo. Run: bash provision/agent/agent_test.sh
set -u
here=$(cd "$(dirname "$0")" && pwd)
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is required to run this test"; exit 1; }
command -v git >/dev/null 2>&1 || { echo "FAIL: git is required to run this test"; exit 1; }

tmp=$(mktemp -d)
trap 'chmod -R u+w "$tmp" 2>/dev/null; rm -rf "$tmp"' EXIT
fail=0
bad() { echo "FAIL $*"; fail=1; }
eq() { [ "$2" = "$3" ] || bad "$1: expected [$3] got [$2]"; }

export GIT_AUTHOR_NAME=Agent GIT_AUTHOR_EMAIL=agent@example.test
export GIT_COMMITTER_NAME=Agent GIT_COMMITTER_EMAIL=agent@example.test
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export JAUNT_REPO=acme/alpha JAUNT_RUN=run-1 JAUNT_DEFAULT_BRANCH=main

# fresh env per case: origin (bare), work (clone), run dir, log dir, tmp dir
mk() {
  c="$tmp/$1"; rm -rf "$c"; mkdir -p "$c/rundir" "$c/log" "$c/tmp"
  git init -q --bare -b main "$c/origin.git"
  git clone -q "$c/origin.git" "$c/work" 2>/dev/null
  git -C "$c/work" checkout -q -b main 2>/dev/null
  echo base >"$c/work/f.txt"
  git -C "$c/work" add f.txt && git -C "$c/work" commit -q -m base
  git -C "$c/work" push -q origin main 2>/dev/null
  git -C "$c/work" checkout -q -b feat/x
  git -C "$c/work" push -q origin feat/x 2>/dev/null
  export JAUNT_RUN_DIR="$c/rundir" JAUNT_WORK="$c/work" JAUNT_LOG_DIR="$c/log" JAUNT_TMP="$c/tmp"
}
want="jauntbox is wrapping up this run (stop requested). Stop the current task now. Write CONTINUATION.md at the repository root with: what you were doing, the current state, next steps, and open questions for the owner. Commit it together with your work to the current branch and push. Do not start new work."

# --- credential helper
mk cred
out=$(printf 's3cret' >"$JAUNT_RUN_DIR/token"; sh "$here/jaunt-git-credential" get </dev/null)
eq cred-token "$out" "$(printf 'username=x-access-token\npassword=s3cret')"
: >"$JAUNT_RUN_DIR/token"
eq cred-empty "$(sh "$here/jaunt-git-credential" get </dev/null)" "quit=1"
rm "$JAUNT_RUN_DIR/token"
eq cred-missing "$(sh "$here/jaunt-git-credential" get </dev/null)" "quit=1"
printf 'x' >"$JAUNT_RUN_DIR/token"
eq cred-store "$(sh "$here/jaunt-git-credential" store </dev/null; echo "rc=$?")" "rc=0"

# --- hook
mk hook
hk() { sh "$here/jaunt-hook" "$1"; }
eq hook-noflag-pre "$(echo '{"session_id":"s1"}' | hk pretool)" ""
eq hook-noflag-stop "$(echo '{"session_id":"s1"}' | hk stop)" ""
echo "stop requested" >"$JAUNT_RUN_DIR/wrapup"
out=$(echo '{"session_id":"s1"}' | hk pretool)
eq hook-pre-event "$(echo "$out" | jq -r .hookSpecificOutput.hookEventName)" PreToolUse
eq hook-pre-text "$(echo "$out" | jq -r .hookSpecificOutput.additionalContext)" "$want"
eq hook-pre-once "$(echo '{"session_id":"s1"}' | hk pretool)" ""
[ -n "$(echo '{"session_id":"s2"}' | hk pretool)" ] || bad "hook-pre-other-session: expected output"
echo dirty >"$JAUNT_WORK/new.txt"
out=$(echo '{"session_id":"s1"}' | hk stop)
eq hook-stop-decision "$(echo "$out" | jq -r .decision)" block
eq hook-stop-reason "$(echo "$out" | jq -r .reason)" "$want"
eq hook-stop-active "$(echo '{"session_id":"s1","stop_hook_active":true}' | hk stop)" ""
echo note >"$JAUNT_WORK/CONTINUATION.md"
git -C "$JAUNT_WORK" add -A && git -C "$JAUNT_WORK" commit -q -m wip
eq hook-stop-unpushed "$(echo '{"session_id":"s1"}' | hk stop | jq -r .decision)" block
git -C "$JAUNT_WORK" push -q origin HEAD 2>/dev/null
eq hook-stop-done "$(echo '{"session_id":"s1"}' | hk stop)" ""

# --- fallback
mk fb
fb() { sh "$here/jaunt-wrapup-fallback" "$@"; }
echo dirty >"$JAUNT_WORK/new.txt"
echo '{"x":1}' >"$JAUNT_LOG_DIR/claude.jsonl"
eq fb-pushed "$(fb "max duration")" pushed
git -C "$c/origin.git" log -1 --format=%s feat/x >"$tmp/subj"
eq fb-subject "$(cat "$tmp/subj")" "wip(jauntbox): mechanical wrap-up (max duration)"
note=$(git -C "$c/origin.git" show feat/x:CONTINUATION.md)
case "$note" in "# CONTINUATION (written by jauntbox)"*) ;; *) bad "fb-title: $note";; esac
case "$note" in *"did not leave a continuation note before the run ended (max duration)."*) ;; *) bad "fb-reason";; esac
case "$note" in *"CONTINUATION.md.tmp"*|*"continuation.tmp"*) bad "fb-tmp-listed";; esac
case "$note" in *"- Branch: feat/x"*"- Run: run-1"*"## git status"*"?? new.txt"*"## Changes against origin/main"*"## Last log lines"*'{"x":1}'*) ;; *) bad "fb-body: $note";; esac
git -C "$c/origin.git" show feat/x:new.txt >/dev/null 2>&1 || bad "fb-new-file-pushed"
eq fb-again "$(fb "max duration")" already-done
mk fb2
echo dirty >"$JAUNT_WORK/new.txt"
git -C "$JAUNT_WORK" remote set-url origin "$c/nonexistent.git"
out=$(fb oops); rc=$?
case "$out" in "failed: "*) ;; *) bad "fb-unreachable output: $out";; esac
eq fb-unreachable-rc "$rc" 1

mk fb3
printf '#!/bin/sh\nexit 1\n' >"$JAUNT_WORK/.git/hooks/pre-commit"; cp "$JAUNT_WORK/.git/hooks/pre-commit" "$JAUNT_WORK/.git/hooks/pre-push"
chmod +x "$JAUNT_WORK/.git/hooks/pre-commit" "$JAUNT_WORK/.git/hooks/pre-push"
echo dirty >"$JAUNT_WORK/new.txt"
eq fb-hooks-bypassed "$(fb hooks)" pushed
# --- unattended
mk un
out=$(JAUNT_PROMPT= sh "$here/jaunt-unattended" 2>&1); rc=$?
[ "$rc" -ne 0 ] || bad "unattended-noprompt rc"
case "$out" in *JAUNT_PROMPT*) ;; *) bad "unattended-noprompt msg: $out";; esac

# --- init
mk init
cat >"$tmp/true" <<'S'
#!/bin/sh
echo "ran:$*" >"$JAUNT_TMP/exec-ran"
S
chmod +x "$tmp/true"
ini() { sh "$here/jaunt-agent-init" "$@"; }
ini "$tmp/true" a b || bad "init-plain rc"
eq init-stays "$(git -C "$JAUNT_WORK" branch --show-current)" feat/x
eq init-exec "$(cat "$JAUNT_TMP/exec-ran")" "ran:a b"
[ -e "$JAUNT_TMP/jauntbox-ready" ] || bad "init-ready marker"
cp "$JAUNT_WORK/.git/config" "$tmp/config.before"
echo wip >"$JAUNT_WORK/uncommitted.txt"
JAUNT_SWITCH_TO=agent/x-20261110 ini "$tmp/true" || bad "init-switch rc"
eq init-switched "$(git -C "$JAUNT_WORK" branch --show-current)" agent/x-20261110
cmp -s "$tmp/config.before" "$JAUNT_WORK/.git/config" || bad "init-config changed"
[ -f "$JAUNT_WORK/uncommitted.txt" ] || bad "init-uncommitted lost"
JAUNT_SWITCH_TO=feat/x ini "$tmp/true" || bad "init-switch-existing rc"
eq init-existing "$(git -C "$JAUNT_WORK" branch --show-current)" feat/x
# refused switch: a tracked file that would be overwritten
git -C "$JAUNT_WORK" checkout -q -b other && echo other >"$JAUNT_WORK/f.txt" && git -C "$JAUNT_WORK" commit -q -am other
git -C "$JAUNT_WORK" checkout -q feat/x
echo local >"$JAUNT_WORK/f.txt"
JAUNT_SWITCH_TO=other ini "$tmp/true"; rc=$?
[ "$rc" -ne 0 ] || bad "init-refused rc"
grep -q "overwritten\|local changes" "$JAUNT_LOG_DIR/init.log" || bad "init-refused log: $(cat "$JAUNT_LOG_DIR/init.log")"
JAUNT_WORK="$tmp/notgit" ini "$tmp/true"; rc=$?
[ "$rc" -ne 0 ] || bad "init-notgit rc"

# --- checkout guard: read-only config, commondir stand-in for the RO mount
mk guard
echo . >"$JAUNT_WORK/.git/commondir"
chmod a-w "$JAUNT_WORK/.git/config"
g() { git -C "$JAUNT_WORK" "$@"; }
cp "$JAUNT_WORK/.git/config" "$tmp/guard.before"
g status >/dev/null 2>&1 || bad "guard-status"
echo more >"$JAUNT_WORK/g.txt"; g add g.txt
g commit -q -m g >/dev/null 2>&1 || bad "guard-commit"
g switch --no-track -c agent/g >/dev/null 2>&1 || bad "guard-switch"
g push origin HEAD >/dev/null 2>&1 || bad "guard-push"
git -C "$c/origin.git" rev-parse agent/g >/dev/null 2>&1 || bad "guard-push did not land"
cmp -s "$tmp/guard.before" "$JAUNT_WORK/.git/config" || bad "guard-config changed"

[ "$fail" = 0 ] && echo "ok"
exit "$fail"
