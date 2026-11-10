#!/usr/bin/env bash
# Provisions the jauntbox as root: creates the engineer user, installs Docker (with
# its data-root on the persistent /home volume so images survive toggles), and base
# tools (git, gh). The language toolchain (asdf/mise + Go/Rust/Ruby/Node) is layered
# on afterwards, as the user, by dev-setup-user.sh.
#
# Idempotent: safe on every boot. Cold first boot is slow (compiles Ruby); warm boots
# (volume already populated) are near-instant because every step is a skip-if-present.
set -euo pipefail

# shellcheck disable=SC1091
. /etc/jaunt/provision.env # JAUNT_USER, JAUNT_UID, JAUNT_INSTALL_TOOLCHAIN, JAUNT_DOCKER_DATA_ROOT

log() { echo "[jaunt-provision] $*"; }

# --- 1. Engineer user -------------------------------------------------------------
# Pinned UID/GID: /home lives on the persistent volume, so a stable id keeps file
# ownership correct when the box is destroyed and recreated.
if ! id "$JAUNT_USER" >/dev/null 2>&1; then
  log "creating user $JAUNT_USER (uid/gid $JAUNT_UID)"
  groupadd -g "$JAUNT_UID" "$JAUNT_USER" 2>/dev/null || true
  useradd -m -u "$JAUNT_UID" -g "$JAUNT_UID" -s /bin/bash "$JAUNT_USER"
fi
# Passwordless sudo for the engineer.
printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$JAUNT_USER" >"/etc/sudoers.d/90-jaunt-$JAUNT_USER"
chmod 0440 "/etc/sudoers.d/90-jaunt-$JAUNT_USER"
# Home may already exist on the volume from a prior box — ensure it's owned correctly.
install -d -o "$JAUNT_USER" -g "$JAUNT_USER" -m 0755 "/home/$JAUNT_USER"
# Copy the cloud-injected SSH key (Hetzner/UpCloud seed it onto root) so `ssh
# <user>@box` works for direct SSH too, not just root.
if [ -f /root/.ssh/authorized_keys ]; then
  install -d -o "$JAUNT_USER" -g "$JAUNT_USER" -m 0700 "/home/$JAUNT_USER/.ssh"
  install -o "$JAUNT_USER" -g "$JAUNT_USER" -m 0600 \
    /root/.ssh/authorized_keys "/home/$JAUNT_USER/.ssh/authorized_keys"
fi

# --- 2. Base packages -------------------------------------------------------------
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
# git + curl/gnupg (repos), jq (Node LTS lookup), zsh (the engineer login shell), fzf +
# ripgrep + fd-find + bat (fuzzy find / Neovim / previews), and the C toolchain + libs that
# ruby-build needs to compile Ruby from source.
apt-get install -y --no-install-recommends \
  git curl ca-certificates gnupg jq unzip zsh fzf ripgrep fd-find bat \
  build-essential autoconf bison \
  libssl-dev libyaml-dev libreadline-dev zlib1g-dev libncurses-dev libffi-dev libgdbm-dev
# Debian/Ubuntu ship these under alternate names; give them their conventional names.
[ -x /usr/bin/fdfind ] && ln -sf /usr/bin/fdfind /usr/local/bin/fd
[ -x /usr/bin/batcat ] && ln -sf /usr/bin/batcat /usr/local/bin/bat
# cloud-init writes the repo picker to /opt/jaunt; put it on PATH as `jaunt-repos`.
[ -x /opt/jaunt/jaunt-repos.sh ] && ln -sf /opt/jaunt/jaunt-repos.sh /usr/local/bin/jaunt-repos
# Make zsh the engineer's login shell (matches the reference devbox + local dev shell). Done
# here, after zsh is installed, rather than at useradd time.
chsh -s "$(command -v zsh)" "$JAUNT_USER"

# GitHub CLI (official apt repo).
if ! command -v gh >/dev/null 2>&1; then
  log "installing gh"
  install -d -m 0755 /etc/apt/keyrings
  curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    -o /etc/apt/keyrings/githubcli-archive-keyring.gpg
  chmod 0644 /etc/apt/keyrings/githubcli-archive-keyring.gpg
  printf 'deb [arch=%s signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main\n' \
    "$(dpkg --print-architecture)" >/etc/apt/sources.list.d/github-cli.list
  apt-get update -y
  apt-get install -y gh
fi

# --- 2b. Neovim (pinned stable; official static tarball -> /opt, symlinked) --------
# The engineer brings their own config (dotfiles repo / rsync) onto the /home volume;
# this installs only the editor + runtime.
NVIM_VERSION="v0.12.4"
if [ "$(nvim --version 2>/dev/null | head -1)" != "NVIM ${NVIM_VERSION}" ]; then
  log "installing neovim ${NVIM_VERSION}"
  nvim_tmp="$(mktemp -d)"
  curl -fsSL "https://github.com/neovim/neovim/releases/download/${NVIM_VERSION}/nvim-linux-x86_64.tar.gz" \
    | tar -xz -C "$nvim_tmp"
  rm -rf /opt/nvim
  mv "$nvim_tmp/nvim-linux-x86_64" /opt/nvim
  ln -sfn /opt/nvim/bin/nvim /usr/local/bin/nvim
  rm -rf "$nvim_tmp"
fi

# --- 2c. Starship prompt (fast, git-aware; matches the reference devbox) -----------
if ! command -v starship >/dev/null 2>&1; then
  log "installing starship"
  curl -sS https://starship.rs/install.sh | sh -s -- --yes >/dev/null
fi

# --- 3. Docker, data-root on the persistent volume --------------------------------
if ! command -v docker >/dev/null 2>&1; then
  log "installing docker"
  curl -fsSL https://get.docker.com | sh
fi
install -d -m 0711 "$JAUNT_DOCKER_DATA_ROOT"
install -d -m 0755 /etc/docker
cat >/etc/docker/daemon.json <<EOF
{ "data-root": "$JAUNT_DOCKER_DATA_ROOT" }
EOF
systemctl enable docker
systemctl restart docker
usermod -aG docker "$JAUNT_USER"

# --- 4. Language toolchain (as the user) ------------------------------------------
if [ "${JAUNT_INSTALL_TOOLCHAIN:-true}" != "true" ]; then
  log "JAUNT_INSTALL_TOOLCHAIN != true — skipping language toolchain"
  exit 0
fi
log "installing language toolchain as $JAUNT_USER (slow on a cold volume)"
su - "$JAUNT_USER" -c "bash /opt/jaunt/dev-setup-user.sh"
# herdr lives in the user's ~/.local/bin; link it where a non-interactive ssh
# command (herdr --remote) finds it on PATH.
if [ -x "/home/$JAUNT_USER/.local/bin/herdr" ]; then
  ln -sf "/home/$JAUNT_USER/.local/bin/herdr" /usr/local/bin/herdr
fi
log "provisioning complete"
