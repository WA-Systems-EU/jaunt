#!/bin/sh
# jauntbox installer.
#   curl -fsSL https://www.jauntbox.eu/install.sh | sh
#   curl -fsSL https://www.jauntbox.eu/install.sh | sh -s -- --version 0.4.0 --dir ~/bin
# Installs the jauntbox laptop client from a GitHub release after checking its
# SHA-256 against the release's checksums.txt. No root needed by default.
set -eu

RELEASE_ROOT=${JAUNTBOX_RELEASE_ROOT:-https://github.com/WA-Systems-EU/jaunt/releases/download}
LATEST_URL=${JAUNTBOX_LATEST_URL:-https://github.com/WA-Systems-EU/jaunt/releases/latest}

die() { echo "install.sh: $*" >&2; exit 1; }

version=
dir=${HOME:-}/.local/bin
while [ $# -gt 0 ]; do
  case $1 in
    --version) [ $# -ge 2 ] || die "--version needs a value"; version=$2; shift 2 ;;
    --dir) [ $# -ge 2 ] || die "--dir needs a value"; dir=$2; shift 2 ;;
    -h|--help) echo "usage: install.sh [--version X.Y.Z] [--dir DIR]"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
version=${version#v}

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  FreeBSD) os=freebsd ;;
  *) die "unsupported operating system: $(uname -s) (jauntbox supports Linux, macOS and FreeBSD)" ;;
esac
case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported architecture: $(uname -m) (jauntbox supports amd64 and arm64)" ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required"

if [ -z "$version" ]; then
  final=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$LATEST_URL") || die "could not find the latest release"
  version=${final##*/}
  version=${version#v}
  [ -n "$version" ] || die "could not read the latest version from $final"
fi

archive="jauntbox_${version}_${os}_${arch}.tar.gz"
base="$RELEASE_ROOT/v$version"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading jauntbox $version for $os/$arch"
curl -fsSL -o "$tmp/$archive" "$base/$archive" || die "could not download $base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "could not download $base/checksums.txt"

want=$(awk -v f="$archive" '$2 == f { print $1; exit }' "$tmp/checksums.txt")
[ -n "$want" ] || die "checksums.txt has no entry for $archive"

if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
elif command -v sha256 >/dev/null 2>&1; then
  got=$(sha256 -q "$tmp/$archive")
else
  die "no SHA-256 tool found (need sha256sum, shasum or sha256)"
fi
[ "$got" = "$want" ] || die "checksum mismatch for $archive (expected $want, got $got); nothing installed"

mkdir "$tmp/x"
tar -xzf "$tmp/$archive" -C "$tmp/x" jauntbox || die "could not unpack $archive"
mkdir -p "$dir" || die "could not create $dir"
if command -v install >/dev/null 2>&1; then
  install -m 0755 "$tmp/x/jauntbox" "$dir/jauntbox" || die "could not write $dir/jauntbox"
else
  cp "$tmp/x/jauntbox" "$dir/jauntbox" && chmod 0755 "$dir/jauntbox" || die "could not write $dir/jauntbox"
fi

echo "Installed $dir/jauntbox"
case ":${PATH:-}:" in
  *":$dir:"*) ;;
  *) echo "Note: $dir is not on your PATH. Add it, for example:"
     echo "  export PATH=\"$dir:\$PATH\"" ;;
esac
"$dir/jauntbox" --version
