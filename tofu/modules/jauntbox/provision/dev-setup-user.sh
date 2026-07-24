#!/usr/bin/env bash
# Installs the language toolchain into the engineer's home (on the persistent volume,
# so it survives box teardown). Run AS the engineer user by dev-setup.sh.
#
# Per design: asdf is the primary version manager; mise is also installed and
# available. Idempotent — re-running only fills in what's missing.
set -euo pipefail

log() { echo "[jaunt-provision:user] $*"; }

# Source builds (ruby-build compiling Ruby, some asdf plugins) scratch in $TMPDIR. The box's
# /tmp is a small RAM-backed tmpfs (~50% of RAM, ~1G here) and overflows on a Ruby compile
# ("No space left on device"). Point TMPDIR at the roomy persistent /home volume instead.
export TMPDIR="$HOME/.cache/jaunt-build-tmp"
mkdir -p "$TMPDIR"

# --- mise (available alongside asdf) ---
if [ ! -x "$HOME/.local/bin/mise" ]; then
  log "installing mise"
  curl -fsSL https://mise.run | sh
fi

# --- asdf (primary) ---
# Pin to v0.15.0, the last shell-based asdf release. v0.16+ rewrote asdf in Go and dropped
# asdf.sh (and changed `asdf global` -> `asdf set`), which breaks the sourcing and command
# usage below. Guard on asdf.sh (not just the dir) so a box left with a broken v0.16 clone
# from an earlier run self-heals instead of being skipped.
if [ ! -f "$HOME/.asdf/asdf.sh" ]; then
  log "installing asdf (v0.15.0, last shell release)"
  rm -rf "$HOME/.asdf"
  git clone --depth 1 --branch v0.15.0 https://github.com/asdf-vm/asdf.git "$HOME/.asdf"
fi
# shellcheck disable=SC1091
. "$HOME/.asdf/asdf.sh"

# Load both on interactive login, once.
if ! grep -q 'jaunt toolchain' "$HOME/.bashrc" 2>/dev/null; then
  cat >>"$HOME/.bashrc" <<'RC'

# jaunt toolchain
export TMPDIR="$HOME/.cache/jaunt-build-tmp"; mkdir -p "$TMPDIR"
. "$HOME/.asdf/asdf.sh"
[ -x "$HOME/.local/bin/mise" ] && eval "$("$HOME/.local/bin/mise" activate bash)"
RC
fi

add_plugin() { asdf plugin list 2>/dev/null | grep -qx "$1" || asdf plugin add "$1"; }

# --- Go (latest) ---
add_plugin golang
go_v="$(asdf latest golang)"
asdf install golang "$go_v"
asdf global golang "$go_v"

# --- Rust (latest) ---
add_plugin rust
rust_v="$(asdf latest rust)"
asdf install rust "$rust_v"
asdf global rust "$rust_v"

# --- Node (latest LTS) ---
add_plugin nodejs
node_v="$(curl -fsSL https://nodejs.org/dist/index.json |
  jq -r 'map(select(.lts != false)) | .[0].version' | sed 's/^v//')"
asdf install nodejs "$node_v"
asdf global nodejs "$node_v"

# --- Ruby (two latest stable MRI releases; global = the newest) ---
add_plugin ruby
rubies="$(asdf list all ruby | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | tail -2)"
for r in $rubies; do asdf install ruby "$r"; done
asdf global ruby "$(printf '%s\n' "$rubies" | tail -1)"

asdf reshim
log "toolchain ready: go $go_v · rust $rust_v · node $node_v · ruby $(printf '%s' "$rubies" | tr '\n' ',')"
