#!/usr/bin/env bash
# jaunt-repos — connect to GitHub, list repos already cloned under ~/src, and interactively
# clone more. Meant to be run by the engineer on the box (manual command). Idempotent:
# repos already present are shown and skipped, never re-cloned.
set -euo pipefail

WORKSPACE="${JAUNT_SRC:-$HOME/src}"
mkdir -p "$WORKSPACE"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }

command -v gh >/dev/null 2>&1 || { echo "gh (GitHub CLI) is not installed." >&2; exit 1; }

# --- 1. Ensure we are connected to GitHub ---------------------------------------------
# On a headless box `gh auth login` uses the device flow: it prints a one-time code and a
# URL for you to open in a browser on your own machine — no browser needed on the box.
if ! gh auth status >/dev/null 2>&1; then
  bold "GitHub: not authenticated — starting login (device flow)"
  gh auth login --hostname github.com --git-protocol https
fi
account="$(gh api user --jq .login 2>/dev/null || echo '?')"
bold "GitHub: authenticated as ${account}"

# --- 2. Show repositories already cloned in the workspace ------------------------------
printf '\n'; bold "Already cloned in ${WORKSPACE}:"
existing=0
while IFS= read -r gitdir; do
  repo_dir="$(dirname "$gitdir")"
  name="${repo_dir#"${WORKSPACE}"/}"
  remote="$(git -C "$repo_dir" remote get-url origin 2>/dev/null || echo 'no origin')"
  info "$(printf '%-40s %s' "$name" "$remote")"
  existing=1
done < <(find "$WORKSPACE" -maxdepth 3 -type d -name .git 2>/dev/null | sort)
[ "$existing" -eq 0 ] && info "(none yet)"

# --- 3. Offer to clone more -----------------------------------------------------------
printf '\n'; read -rp "Clone more repositories? [y/N] " ans
case "${ans:-}" in [yY]*) ;; *) bold "Nothing to do."; exit 0 ;; esac

read -rp "Owner/org to list [${account}]: " owner
owner="${owner:-$account}"

bold "Fetching repositories for ${owner} ..."
mapfile -t repos < <(gh repo list "$owner" --limit 200 \
  --json nameWithOwner,description --jq '.[] | "\(.nameWithOwner)\t\(.description // "")"')
if [ "${#repos[@]}" -eq 0 ]; then
  info "No repositories found for ${owner}."; exit 0
fi

i=1
for r in "${repos[@]}"; do
  nwo="${r%%$'\t'*}"; desc="${r#*$'\t'}"
  printf "  %3d) %-45s %s\n" "$i" "$nwo" "$desc"
  i=$((i + 1))
done

printf '\n'; read -rp "Numbers to clone (space-separated, or 'all', blank to skip): " picks
[ "${picks:-}" = "all" ] && picks="$(seq 1 "${#repos[@]}")"

for n in ${picks:-}; do
  case "$n" in '' | *[!0-9]*) continue ;; esac
  idx=$((n - 1))
  { [ "$idx" -lt 0 ] || [ "$idx" -ge "${#repos[@]}" ]; } && continue
  nwo="${repos[$idx]%%$'\t'*}"
  dest="${WORKSPACE}/${nwo##*/}"
  if [ -d "$dest" ]; then info "skip ${nwo} (already at ${dest})"; continue; fi
  bold "Cloning ${nwo} -> ${dest}"
  gh repo clone "$nwo" "$dest"
done

printf '\n'; bold "Repositories in ${WORKSPACE}:"
find "$WORKSPACE" -maxdepth 3 -type d -name .git -printf '  %h\n' 2>/dev/null | sed "s#${WORKSPACE}/##" | sort
