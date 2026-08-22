#!/bin/sh
# pstack installer — one static binary from GitHub Releases.
#
#   curl -fsSL https://github.com/samishal1998/preview-stacks/releases/latest/download/install.sh | sh
#
#   PSTACK_VERSION=0.29.0      pin a version (default: the release this script came from, else latest)
#   PSTACK_INSTALL_DIR=~/bin   where to put it (default /usr/local/bin; no sudo is attempted)
#
# The whole script is one function called at the very end, so a download cut off half-way runs
# nothing. The binary is checksum-verified against the release's checksums.txt before it is moved
# into place, and the move is atomic (cp → chmod → mv) so a concurrent `pstack` never sees a
# half-written file. Nothing else is installed and PATH is not edited.
set -eu

main() {
  repo="samishal1998/preview-stacks"
  version="${PSTACK_VERSION:-__VERSION__}"
  dir="${PSTACK_INSTALL_DIR:-/usr/local/bin}"

  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$os" in
    linux|darwin) ;;
    *) echo "pstack: unsupported OS '$os' (linux and darwin are released)" >&2; exit 1 ;;
  esac
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo "pstack: unsupported architecture '$arch' (amd64 and arm64 are released)" >&2; exit 1 ;;
  esac

  if [ "$version" = "latest" ] || [ "$version" = "__VERSION__" ]; then
    base="https://github.com/$repo/releases/latest/download"
  else
    version="${version#v}"
    base="https://github.com/$repo/releases/download/v$version"
  fi
  asset="pstack_${os}_${arch}"

  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT INT TERM

  echo "pstack: downloading $asset ($([ "$version" = "__VERSION__" ] && echo latest || echo "$version"))" >&2
  curl -fsSL -o "$tmp/$asset" "$base/$asset"
  curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"

  expected="$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
  if [ -z "$expected" ]; then
    echo "pstack: $asset is not listed in checksums.txt — refusing to install an unverified binary" >&2
    exit 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$tmp/$asset" | cut -d' ' -f1)"
  else
    actual="$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)"
  fi
  if [ "$actual" != "$expected" ]; then
    echo "pstack: checksum mismatch for $asset (expected $expected, got $actual)" >&2
    exit 1
  fi

  mkdir -p "$dir"
  cp "$tmp/$asset" "$tmp/pstack"
  chmod 0755 "$tmp/pstack"
  mv -f "$tmp/pstack" "$dir/pstack"
  echo "pstack: installed $("$dir/pstack" --version) to $dir/pstack" >&2
  case ":$PATH:" in
    *":$dir:"*) ;;
    *) echo "pstack: note — $dir is not on your PATH" >&2 ;;
  esac
}

main "$@"
