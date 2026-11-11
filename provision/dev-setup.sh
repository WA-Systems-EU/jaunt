#!/usr/bin/env bash
# Provisions the jauntbox as root: creates the engineer user, installs Docker (with
# its data-root on the persistent /home volume so images survive toggles), and base
# tools (git, gh). The language toolchain (asdf/mise + Go/Rust/Ruby/Node) is layered
# on afterwards, as the user, by dev-setup-user.sh.
#
# Idempotent: safe on every boot. Cold first boot is slow (compiles Ruby); warm boots
# (volume already populated) are near-instant because every step is a skip-if-present.

log() { echo "[jaunt-provision] $*"; }

# write_sudoers <user> <dir>: password sudo for the engineer, passwordless only for the
# few jauntctl commands the box-side helpers need. The NOPASSWD line comes last because
# sudo uses the last matching entry. Validated with visudo before it replaces the
# current file; on failure the previous file stays and provisioning continues.
write_sudoers() {
  local user=$1 dir=$2 tmp
  tmp=$(mktemp "$dir/.90-jaunt-$user.XXXXXX") || { log "WARNING: cannot create sudoers temp file in $dir"; return 0; }
  {
    printf '%s ALL=(ALL:ALL) ALL\n' "$user"
    printf '%s ALL=(root) NOPASSWD: /usr/local/bin/jauntctl mint *, /usr/local/bin/jauntctl repos, /usr/local/bin/jauntctl repos *, /usr/local/bin/jauntctl box-status, /usr/local/bin/jauntctl box-status *, /usr/local/bin/jauntctl agent *\n' "$user"
  } >"$tmp"
  chmod 0440 "$tmp"
  if visudo -cf "$tmp" >/dev/null 2>&1; then
    mv -f "$tmp" "$dir/90-jaunt-$user"
  else
    rm -f "$tmp"
    log "WARNING: sudoers for $user failed visudo check; keeping the previous file"
  fi
  return 0
}

# setup_rootless_docker <user> <uid> <etc-dir>: gives the engineer their own rootless
# Docker daemon (the docker group is root-equivalent, so they are not in it). Best
# effort: every step failing logs a WARNING and returns 0, so provisioning goes on and
# `sudo docker` still works. <etc-dir> is /etc (tests point it at a temp dir);
# JAUNT_RUN_USER_DIR overrides /run/user (test-only; production uses /run/user). The
# optional 4th arg is the user's home (tests only; default from getent).
setup_rootless_docker() {
  local user=$1 uid=$2 etc=$3 home=${4:-}
  local rdir="${JAUNT_RUN_USER_DIR:-/run/user}/$uid" i
  local -a asuser=(runuser -u "$user" -- env "XDG_RUNTIME_DIR=/run/user/$uid" "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus")

  apt-get install -y --no-install-recommends uidmap dbus-user-session slirp4netns \
    || { log "WARNING: rootless docker setup failed: packages"; return 0; }
  apt-get install -y --no-install-recommends docker-ce-rootless-extras \
    || { log "WARNING: rootless docker setup failed: docker-ce-rootless-extras"; return 0; }

  local f
  for f in subuid subgid; do
    if ! grep -q "^$user:" "$etc/$f" 2>/dev/null; then
      printf '%s:100000:65536\n' "$user" >>"$etc/$f" \
        || { log "WARNING: rootless docker setup failed: $f"; return 0; }
    fi
  done

  # Ubuntu 24.04 restricts unprivileged user namespaces; rootlesskit gets a profile
  # that allows them (the sysctl stays on). Profile text is from Docker's rootless docs
  # for Ubuntu 24.04.
  mkdir -p "$etc/apparmor.d" || { log "WARNING: rootless docker setup failed: apparmor dir"; return 0; }
  cat >"$etc/apparmor.d/usr.bin.rootlesskit" <<'PROFILE' \
    || { log "WARNING: rootless docker setup failed: apparmor profile"; return 0; }
abi <abi/4.0>,
include <tunables/global>

profile rootlesskit /usr/bin/rootlesskit flags=(unconfined) {
  userns,
  include if exists <local/usr.bin.rootlesskit>
}
PROFILE
  apparmor_parser -r "$etc/apparmor.d/usr.bin.rootlesskit" \
    || { log "WARNING: rootless docker setup failed: apparmor_parser"; return 0; }

  loginctl enable-linger "$user" \
    || { log "WARNING: rootless docker setup failed: enable-linger"; return 0; }
  # enable-linger creates /run/user/<uid> before user@<uid>.service and its bus exist;
  # restart the manager synchronously (it may already run without dbus-user-session's bus), then wait for the bus socket.
  systemctl restart "user@$uid.service" \
    || { log "WARNING: rootless docker setup failed: systemctl restart user@$uid.service"; return 0; }
  for i in $(seq 1 30); do [ -S "$rdir/bus" ] && break; sleep 1; done
  [ -S "$rdir/bus" ] || { log "WARNING: rootless docker setup failed: $rdir/bus did not appear"; return 0; }

  if [ -z "$home" ]; then
    home=$(getent passwd "$user" | cut -d: -f6) \
      || { log "WARNING: rootless docker setup failed: home lookup"; return 0; }
    home=${home:-/home/$user}
  fi
  if [ ! -f "$home/.config/systemd/user/docker.service" ]; then
    "${asuser[@]}" dockerd-rootless-setuptool.sh install \
      || { log "WARNING: rootless docker setup failed: dockerd-rootless-setuptool.sh install"; return 0; }
  fi
  "${asuser[@]}" systemctl --user enable --now docker \
    || { log "WARNING: rootless docker setup failed: systemctl --user enable --now docker"; return 0; }
  # A reused /home may have the unit but no ~/.docker; make sure the context exists.
  "${asuser[@]}" docker context create rootless --docker "host=unix:///run/user/$uid/docker.sock" || true
  "${asuser[@]}" docker context use rootless \
    || { log "WARNING: rootless docker setup failed: docker context use rootless"; return 0; }
  return 0
}

# When sourced (tests), stop here: only the functions above are defined.
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then return 0; fi

set -euo pipefail

# shellcheck disable=SC1091
. /etc/jaunt/provision.env # JAUNT_USER, JAUNT_UID, JAUNT_INSTALL_TOOLCHAIN, JAUNT_DOCKER_DATA_ROOT

# --- 1. Engineer user -------------------------------------------------------------
# Pinned UID/GID: /home lives on the persistent volume, so a stable id keeps file
# ownership correct when the box is destroyed and recreated.
if ! id "$JAUNT_USER" >/dev/null 2>&1; then
  log "creating user $JAUNT_USER (uid/gid $JAUNT_UID)"
  groupadd -g "$JAUNT_UID" "$JAUNT_USER" 2>/dev/null || true
  useradd -m -u "$JAUNT_UID" -g "$JAUNT_UID" -s /bin/bash "$JAUNT_USER"
fi
# Password sudo for the engineer; passwordless only for a few jauntctl commands.
write_sudoers "$JAUNT_USER" /etc/sudoers.d
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
# The engineer is not in the docker group (docker access is root-equivalent).
gpasswd -d "$JAUNT_USER" docker 2>/dev/null || true

# --- 3b. Rootless Docker for the engineer (own daemon, data on the /home volume) ----
setup_rootless_docker "$JAUNT_USER" "$JAUNT_UID" /etc

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
