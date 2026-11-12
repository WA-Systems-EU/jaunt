#!/bin/sh
# Print the body of one version's CHANGELOG.md section (without its heading).
# Usage: changelog-section.sh <version>   (version without the leading v)
# A pre-release such as 0.5.0-rc.1 uses the 0.5.0 section unless it has its own.
# Exit 1 when the changelog has no section for it.
set -eu
[ $# -eq 1 ] || { echo "usage: changelog-section.sh <version>" >&2; exit 2; }
version=$1
here=$(cd "$(dirname "$0")" && pwd)
file=${CHANGELOG_FILE:-$here/../CHANGELOG.md}

section() {
  awk -v want="$1" '
    /^## \[/ {
      if (found) exit
      h = $0
      sub(/^## \[/, "", h)
      sub(/\].*/, "", h)
      if (h == want) { found = 1 }
      next
    }
    found { print }
  ' "$file"
}

out=$(section "$version")
# A pre-release (0.5.0-rc.1) falls back to its base version's section.
case $version in
  *-*)
    if [ -z "$(printf '%s' "$out" | tr -d ' \t\n')" ]; then
      out=$(section "${version%%-*}")
    fi ;;
esac

if [ -z "$(printf '%s' "$out" | tr -d ' \t\n')" ]; then
  echo "changelog-section: no section for $version in $file" >&2
  exit 1
fi
printf '%s\n' "$out" | sed -e '/./,$!d'
