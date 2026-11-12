#!/bin/sh
# Print the body of one version's CHANGELOG.md section (without its heading).
# Usage: changelog-section.sh <version>   (version without the leading v)
# Exit 1 when the changelog has no section for it.
set -eu
[ $# -eq 1 ] || { echo "usage: changelog-section.sh <version>" >&2; exit 2; }
version=$1
here=$(cd "$(dirname "$0")" && pwd)
file=${CHANGELOG_FILE:-$here/../CHANGELOG.md}

out=$(awk -v want="$version" '
  /^## \[/ {
    if (found) exit
    h = $0
    sub(/^## \[/, "", h)
    sub(/\].*/, "", h)
    if (h == want) { found = 1 }
    next
  }
  found { print }
' "$file")

if [ -z "$(printf '%s' "$out" | tr -d ' \t\n')" ]; then
  echo "changelog-section: no section for $version in $file" >&2
  exit 1
fi
printf '%s\n' "$out" | sed -e '/./,$!d'
