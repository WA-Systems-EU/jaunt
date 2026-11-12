#!/bin/sh
# Tests for site/install.sh using fixture archives served over file://.
# Never touches the network.
set -u
here=$(cd "$(dirname "$0")" && pwd)
script="$here/../site/install.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
ok() { echo "ok   $1"; }
bad() { echo "FAIL $1"; fail=1; }

VER=0.0.0-test
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $(uname -m) in x86_64|amd64) arch=amd64 ;; *) arch=arm64 ;; esac
archive="jauntbox_${VER}_${os}_${arch}.tar.gz"

root="$tmp/root"
mkdir -p "$root/v$VER" "$root/latest" "$tmp/stage"
printf '#!/bin/sh\necho "jauntbox 0.0.0-test (commit abc1234, built 2026-01-01)"\n' > "$tmp/stage/jauntbox"
chmod 0755 "$tmp/stage/jauntbox"
echo license > "$tmp/stage/LICENSE"
echo readme > "$tmp/stage/README.md"
tar -czf "$root/v$VER/$archive" -C "$tmp/stage" jauntbox LICENSE README.md

sha_of() { sha256sum "$1" | cut -d' ' -f1; }
mksums() {
  { printf '%s  %s\n' "$(sha_of "$root/v$VER/$archive")" "$archive"
    printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "jauntbox_${VER}_plan9_amd64.tar.gz"
  } > "$root/v$VER/checksums.txt"
}
mksums
good_archive="$tmp/good.tar.gz"
cp "$root/v$VER/$archive" "$good_archive"

: > "$root/latest/v$VER"
export JAUNTBOX_RELEASE_ROOT="file://$root"
export JAUNTBOX_LATEST_URL="file://$root/latest/v$VER"

# 1. pinned install
bin1="$tmp/bin1"
out=$(sh "$script" --version "$VER" --dir "$bin1" 2>&1); rc=$?
if [ "$rc" -eq 0 ] && [ -x "$bin1/jauntbox" ] && "$bin1/jauntbox" --version | grep -q '^jauntbox 0.0.0-test'; then ok "pinned install"; else bad "pinned install (rc=$rc): $out"; fi
case $out in *"jauntbox 0.0.0-test"*) ok "prints version" ;; *) bad "does not print version: $out" ;; esac
case $out in *PATH*) ok "off-PATH dir mentions PATH" ;; *) bad "no PATH hint: $out" ;; esac

# 2. latest resolution
bin2="$tmp/bin2"
out=$(sh "$script" --dir "$bin2" 2>&1); rc=$?
if [ "$rc" -eq 0 ] && [ -x "$bin2/jauntbox" ]; then ok "latest resolution"; else bad "latest resolution (rc=$rc): $out"; fi

# 3. dir on PATH: no hint
bin3="$tmp/bin3"
out=$(PATH="$bin3:$PATH" sh "$script" --version "$VER" --dir "$bin3" 2>&1); rc=$?
case $out in *"add "*PATH*|*"Add "*PATH*) bad "PATH hint although dir on PATH" ;; *) [ "$rc" -eq 0 ] && ok "on-PATH dir: no hint" || bad "on-PATH install failed: $out" ;; esac

# 4. corrupted archive
printf 'garbage' >> "$root/v$VER/$archive"
bin4="$tmp/bin4"
out=$(sh "$script" --version "$VER" --dir "$bin4" 2>&1); rc=$?
if [ "$rc" -ne 0 ] && [ ! -e "$bin4/jauntbox" ]; then ok "corrupted archive rejected"; else bad "corrupted archive (rc=$rc)"; fi
case $out in *heck*) ok "mismatch message" ;; *) bad "no checksum message: $out" ;; esac
cp "$good_archive" "$root/v$VER/$archive"

# 5. checksum tool shims: only shasum, only sha256
mkshim() { # name dir body
  mkdir -p "$2"; printf '#!/bin/sh\n%s\n' "$3" > "$2/$1"; chmod +x "$2/$1"
}
# a PATH of symlinks to the tools the script needs, excluding every sha tool
mkbase() {
  d=$1; mkdir -p "$d"
  for t in sh gzip gunzip uname tr cut curl tar mkdir install cp chmod rm mktemp grep awk sed cat head tail dirname basename printf echo id mv ln sort tee wc test expr; do
    p=$(command -v "$t" 2>/dev/null) && case $p in /*) ln -sf "$p" "$d/$t" ;; esac
  done
}
real_sha=$(command -v sha256sum)
shim_test() { # label shimname body
  b="$tmp/path-$1"; mkbase "$b"
  mkshim "$2" "$b" "$3"
  bindir="$tmp/bin-$1"
  out=$(PATH="$b" /bin/sh "$script" --version "$VER" --dir "$bindir" 2>&1); rc=$?
  if [ "$rc" -eq 0 ] && [ -x "$bindir/jauntbox" ]; then ok "checksum via $2"; else bad "checksum via $2 (rc=$rc): $out"; fi
  # and a corrupted one must still fail
  printf 'garbage' >> "$root/v$VER/$archive"
  bindir2="$tmp/bin-$1-bad"
  PATH="$b" /bin/sh "$script" --version "$VER" --dir "$bindir2" >/dev/null 2>&1; rc=$?
  if [ "$rc" -ne 0 ] && [ ! -e "$bindir2/jauntbox" ]; then ok "corruption caught via $2"; else bad "corruption not caught via $2"; fi
  cp "$good_archive" "$root/v$VER/$archive"
}
shim_test shasum shasum "[ \"\$1\" = -a ] && [ \"\$2\" = 256 ] || exit 2; shift 2; exec $real_sha \"\$@\""
shim_test sha256 sha256 "[ \"\$1\" = -q ] || exit 2; shift; f=\$1; h=\$($real_sha \"\$f\" | cut -d' ' -f1); echo \"\$h\""

# 6. unsupported OS
b="$tmp/path-uname"; mkbase "$b"; rm -f "$b/uname"
mkshim uname "$b" 'case $1 in -s) echo MINGW64_NT-10.0;; -m) echo x86_64;; esac'
out=$(PATH="$b:$PATH" sh "$script" --version "$VER" --dir "$tmp/bin6" 2>&1); rc=$?
if [ "$rc" -ne 0 ] && [ ! -e "$tmp/bin6/jauntbox" ]; then ok "unsupported OS fails"; else bad "unsupported OS (rc=$rc)"; fi
case $out in *nsupported*) ok "unsupported message" ;; *) bad "no 'unsupported': $out" ;; esac

# 7. unsupported arch
b="$tmp/path-arch"; mkbase "$b"; rm -f "$b/uname"
mkshim uname "$b" 'case $1 in -s) echo Linux;; -m) echo riscv64;; esac'
out=$(PATH="$b:$PATH" sh "$script" --version "$VER" --dir "$tmp/bin7" 2>&1); rc=$?
if [ "$rc" -ne 0 ]; then ok "unsupported arch fails"; else bad "unsupported arch"; fi

# 8. bad flag
sh "$script" --bogus >/dev/null 2>&1 && bad "unknown flag accepted" || ok "unknown flag rejected"

[ "$fail" -eq 0 ] && echo "install_test: PASS"
exit "$fail"
