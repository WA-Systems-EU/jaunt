FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive DISABLE_AUTOUPDATER=1
ENV MISE_TRUSTED_CONFIG_PATHS=/work PATH=/home/agent/.local/share/mise/shims:$PATH
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl jq ripgrep less vim-tiny build-essential tini libnss-wrapper \
 && rm -rf /var/lib/apt/lists/*
# mise, installed system-wide
RUN curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh
# Claude Code: the native installer installs under $HOME, so give it a staging HOME and move the
# result to /opt/claude (world-readable). Nothing may stay under /root: containers run as an
# arbitrary non-root uid with HOME=/home/agent.
RUN mkdir -p /tmp/claude-home /opt/claude \
 && HOME=/tmp/claude-home sh -c 'curl -fsSL https://claude.ai/install.sh | bash' \
 && cp -a /tmp/claude-home/.local/share/claude/. /opt/claude/ \
 && ln -sf "$(ls -d /opt/claude/versions/* | sort -V | tail -n 1)" /usr/local/bin/claude \
 && rm -rf /tmp/claude-home \
 && chmod -R a+rX /opt/claude
RUN printf '%s\n' \
  '[credential "https://github.com"]' \
  '	helper = /usr/local/bin/jaunt-git-credential' \
  '	useHttpPath = true' \
  '[push]' \
  '	default = current' \
  '[safe]' \
  '	directory = /work' > /etc/gitconfig
COPY managed-settings.json /etc/claude-code/managed-settings.json
RUN chmod 0644 /etc/claude-code/managed-settings.json
COPY jaunt-agent-init jaunt-git-credential jaunt-hook jaunt-unattended jaunt-wrapup-fallback /usr/local/bin/
RUN chmod 0755 /usr/local/bin/jaunt-*
WORKDIR /work
# names for the run's arbitrary uid; jaunt-agent-init writes these files (set after the last RUN so the build is unaffected)
# A docker exec before init has written the files sees no names (harmless).
ENV LD_PRELOAD=libnss_wrapper.so NSS_WRAPPER_PASSWD=/tmp/jauntbox-passwd NSS_WRAPPER_GROUP=/tmp/jauntbox-group
ENTRYPOINT ["/usr/local/bin/jaunt-agent-init"]
CMD ["sleep","infinity"]
