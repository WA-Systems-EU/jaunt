#!/usr/bin/env bash
# jaunt-repos — list repos already cloned under ~/src, list the repositories the jauntbox
# GitHub App is installed on, and interactively clone more. Meant to be run by the engineer
# on the box (manual command). Idempotent: repos already present are shown and skipped.
# Listing goes through `jauntctl repos`; cloning works because the git credential helper
# (`jauntctl git-credential`) mints a one-repo token on demand. No `gh` is used.
set -euo pipefail

WORKSPACE="${JAUNT_SRC:-$HOME/src}"
mkdir -p "$WORKSPACE"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }

if [ -f "$HOME/.config/gh/hosts.yml" ]; then
  echo 'warning: an account-wide GitHub login (gh auth login) is stored on this volume; run "gh auth logout" and revoke it at https://github.com/settings/applications' >&2
fi

# --- 1. Show repositories already cloned in the workspace ------------------------------
bold "Already cloned in ${WORKSPACE}:"
existing=0
while IFS= read -r gitdir; do
  repo_dir="$(dirname "$gitdir")"
  name="${repo_dir#"${WORKSPACE}"/}"
  remote="$(git -C "$repo_dir" remote get-url origin 2>/dev/null || echo 'no origin')"
  info "$(printf '%-40s %s' "$name" "$remote")"
  existing=1
done < <(find "$WORKSPACE" -maxdepth 3 -type d -name .git 2>/dev/null | sort)
[ "$existing" -eq 0 ] && info "(none yet)"

# --- 2. List the App's repositories not yet cloned --------------------------------------
errfile="$(mktemp)"
trap 'rm -f "$errfile"' EXIT
if ! json="$(sudo -n jauntctl repos --json 2>"$errfile")"; then
  cat "$errfile" >&2
  exit 1
fi

# Each entry: name <TAB> owner <TAB> default_branch <TAB> clone_url
candidates=()
while IFS=$'\t' read -r name owner branch url; do
  [ -d "${WORKSPACE}/${name}" ] && continue
  candidates+=("${name}"$'\t'"${owner}"$'\t'"${branch}"$'\t'"${url}")
done < <(printf '%s' "$json" | jq -r '.[] | [.name, .owner, .default_branch, .clone_url] | @tsv')

printf '\n'
if [ "${#candidates[@]}" -eq 0 ]; then
  info "No more repositories to clone from the GitHub App."
  exit 0
fi

bold "Repositories available through the GitHub App:"
i=1
for c in "${candidates[@]}"; do
  IFS=$'\t' read -r name owner branch _ <<<"$c"
  printf "  %3d) %-45s %s\n" "$i" "${owner}/${name}" "(${branch})"
  i=$((i + 1))
done

# --- 3. Clone the picks -----------------------------------------------------------------
printf '\n'; read -rp "Numbers to clone (space-separated, or 'all', blank to skip): " picks || picks=""
[ "${picks:-}" = "all" ] && picks="$(seq 1 "${#candidates[@]}")"

for n in ${picks:-}; do
  case "$n" in '' | *[!0-9]*) continue ;; esac
  idx=$((n - 1))
  { [ "$idx" -lt 0 ] || [ "$idx" -ge "${#candidates[@]}" ]; } && continue
  IFS=$'\t' read -r name owner _ url <<<"${candidates[$idx]}"
  dest="${WORKSPACE}/${name}"
  if [ -d "$dest" ]; then info "skip ${owner}/${name} (already at ${dest})"; continue; fi
  bold "Cloning ${owner}/${name} -> ${dest}"
  git clone "$url" "$dest"
done

printf '\n'; bold "Repositories in ${WORKSPACE}:"
find "$WORKSPACE" -maxdepth 3 -type d -name .git -printf '  %h\n' 2>/dev/null | sed "s#${WORKSPACE}/##" | sort
