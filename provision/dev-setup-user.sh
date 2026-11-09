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
export PATH="$HOME/.local/bin:$PATH"   # mise, claude, and other user-installed CLIs
export TMPDIR="$HOME/.cache/jaunt-build-tmp"; mkdir -p "$TMPDIR"
. "$HOME/.asdf/asdf.sh"
[ -x "$HOME/.local/bin/mise" ] && eval "$("$HOME/.local/bin/mise" activate bash)"
RC
fi

# zsh is the engineer's login shell (see dev-setup.sh). Provide a working default .zshrc —
# toolchain + fzf Ctrl-T/Ctrl-R + starship — appended once. An engineer who brings their own
# dotfiles onto the volume keeps them; this block just adds jaunt's tools (guarded, no dupes).
if ! grep -q 'jaunt toolchain' "$HOME/.zshrc" 2>/dev/null; then
  cat >>"$HOME/.zshrc" <<'RC'

# jaunt toolchain
export PATH="$HOME/.local/bin:$PATH"   # mise, claude, and other user-installed CLIs
export TMPDIR="$HOME/.cache/jaunt-build-tmp"; mkdir -p "$TMPDIR"
# sane zsh history so Ctrl-R has something to search on a bare shell
export HISTFILE="$HOME/.zsh_history" HISTSIZE=100000 SAVEHIST=100000
setopt SHARE_HISTORY HIST_IGNORE_DUPS
. "$HOME/.asdf/asdf.sh"
[ -x "$HOME/.local/bin/mise" ] && eval "$("$HOME/.local/bin/mise" activate zsh)"
# fzf — Ctrl-T (files, fd + bat preview) and Ctrl-R (history), matching the local dev shell
export FZF_DEFAULT_COMMAND='fd --type f --hidden --follow --exclude .git'
export FZF_CTRL_T_OPTS="--preview 'bat --color=always {}' --preview-window=right:50%"
export FZF_CTRL_R_OPTS="--sort --exact"
if command -v fzf >/dev/null; then
  if fzf --zsh >/dev/null 2>&1; then source <(fzf --zsh)
  elif [ -f /usr/share/doc/fzf/examples/key-bindings.zsh ]; then
    source /usr/share/doc/fzf/examples/key-bindings.zsh
  fi
fi
command -v starship >/dev/null && eval "$(starship init zsh)"
RC
fi

add_plugin() { asdf plugin list 2>/dev/null | grep -qx "$1" || asdf plugin add "$1"; }

# --- Go (latest) ---
add_plugin golang
go_v="$(asdf latest golang)"
asdf install golang "$go_v"
asdf global golang "$go_v"

# --- git-appraise (distributed code review stored in git notes) ---
go install github.com/google/git-appraise/git-appraise@latest

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

# --- Claude Code (native CLI -> ~/.local/bin/claude; no Node runtime needed) ---
# Install only. Auth is per-engineer and intentionally NOT baked in (no secrets in
# cloud-init): on the box, set ANTHROPIC_API_KEY, or generate a token with
# `claude setup-token` on a machine that has a browser and export CLAUDE_CODE_OAUTH_TOKEN.
if [ ! -x "$HOME/.local/bin/claude" ]; then
  log "installing Claude Code"
  curl -fsSL https://claude.ai/install.sh | bash
fi

# --- herdr (persistent terminal session for `jauntbox`; ~/.local/bin/herdr) ---
# Pinned: the laptop's `herdr --remote` needs a matching version. herdr.dev's install.sh only
# installs the latest release, so for a pin we take the same per-version binary from the same
# manifest (https://herdr.dev/latest.json), fail-closed on its sha256. The default pin carries a
# digest here (also published as the GitHub release asset digest); override both together with
# JAUNT_HERDR_VERSION / JAUNT_HERDR_SHA256, else the manifest's sha256 for that version is used.
# A failure here only warns: the box still works over plain ssh.
install_herdr() {
  local v="${JAUNT_HERDR_VERSION:-0.7.5}" want="${JAUNT_HERDR_SHA256:-}" manifest url tmp
  if [ -z "$want" ] && [ "$v" = "0.7.5" ]; then
    want="3dc83288073e4c2d3c679a30e7be97bcca9141c6fd17dbbb9219142e95c59253"
  fi
  if [ "$("$HOME/.local/bin/herdr" --version 2>/dev/null | awk '{print $2}')" = "$v" ]; then return 0; fi
  log "installing herdr $v"
  manifest="$(curl -fsSL --retry 3 https://herdr.dev/latest.json)" || { log "WARNING: cannot fetch the herdr manifest"; return 1; }
  url="$(printf '%s' "$manifest" | jq -r --arg v "$v" '.releases[$v].assets["linux-x86_64"] // empty')"
  [ -n "$url" ] || { log "WARNING: herdr $v is not in the herdr.dev manifest"; return 1; }
  if [ -z "$want" ]; then
    want="$(printf '%s' "$manifest" | jq -r --arg v "$v" '.releases[$v].sha256["linux-x86_64"] // empty')"
  fi
  [ -n "$want" ] || { log "WARNING: no sha256 known for herdr $v; set JAUNT_HERDR_SHA256 (refusing to install unverified)"; return 1; }
  tmp="$(mktemp)"
  curl -fsSL --retry 3 "$url" -o "$tmp" || { rm -f "$tmp"; log "WARNING: herdr download failed"; return 1; }
  if [ "$(sha256sum "$tmp" | cut -d' ' -f1)" != "$want" ]; then
    rm -f "$tmp"; log "WARNING: herdr checksum mismatch; not installing"; return 1
  fi
  mkdir -p "$HOME/.local/bin" || return 1
  install -m 0755 "$tmp" "$HOME/.local/bin/herdr" || { rm -f "$tmp"; return 1; }
  rm -f "$tmp"
}
install_herdr || log "herdr not installed; bare 'jauntbox' will not work until it is (the box is usable over ssh)"
