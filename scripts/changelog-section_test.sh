#!/bin/sh
# Tests for changelog-section.sh against a fixture changelog.
set -u
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

cat > "$tmp/CHANGELOG.md" <<'CL'
# Changelog

## [Unreleased]

## [0.4.1] - 2026-12-01

Patch notes.

## [0.4.0] - 2026-11-13

First summary line.

### Upgrade notes

Do the thing.

## [0.3.0] - 2026-01-01

Old stuff.
CL

out=$(CHANGELOG_FILE="$tmp/CHANGELOG.md" sh "$here/changelog-section.sh" 0.4.0); rc=$?
[ "$rc" -eq 0 ] || { echo "FAIL 0.4.0 exit $rc"; fail=1; }
case $out in *"First summary line."*) ;; *) echo "FAIL missing summary"; fail=1 ;; esac
case $out in *"Do the thing."*) ;; *) echo "FAIL missing upgrade notes"; fail=1 ;; esac
case $out in *"## ["*) echo "FAIL contains a version heading"; fail=1 ;; esac
case $out in *"Patch notes."*|*"Old stuff."*) echo "FAIL leaked neighbour"; fail=1 ;; esac

out=$(CHANGELOG_FILE="$tmp/CHANGELOG.md" sh "$here/changelog-section.sh" 9.9.9 2>/dev/null); rc=$?
[ "$rc" -eq 1 ] || { echo "FAIL 9.9.9 exit $rc (want 1)"; fail=1; }
[ -z "$out" ] || { echo "FAIL 9.9.9 printed output"; fail=1; }

# the real changelog must have 0.4.0
sh "$here/changelog-section.sh" 0.4.0 >/dev/null 2>&1 || { echo "FAIL real CHANGELOG lacks 0.4.0"; fail=1; }

[ "$fail" -eq 0 ] && echo "changelog-section_test: PASS"
exit "$fail"
