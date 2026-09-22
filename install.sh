#!/bin/sh
# Install the latest Lectern release for this machine.
#
#   curl -fsSL https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.sh | sh
#
# Picks the archive for this OS/CPU from the latest GitHub release, verifies
# it against the release's checksums.txt, and installs `lectern` into
# /usr/local/bin (with sudo when needed) or ~/.local/bin (LECTERN_INSTALL_DIR
# overrides). Set LECTERN_VERSION=vX.Y.Z to pin a release. Nothing else is
# written and no service is started; `lectern serve` is yours to run.
set -eu

repo="JeremiahM37/lectern"
name="lectern"
version="${LECTERN_VERSION:-latest}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  mingw*|msys*|cygwin*) echo "On Windows use PowerShell: irm https://raw.githubusercontent.com/$repo/main/install.ps1 | iex" >&2; exit 1 ;;
  *) echo "unsupported OS: $os" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported CPU: $(uname -m)" >&2; exit 1 ;;
esac

if [ "$version" = latest ]; then
  base="https://github.com/$repo/releases/latest/download"
else
  base="https://github.com/$repo/releases/download/$version"
fi
archive="${name}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "Downloading $archive ($version)…"
curl -fsSL -o "$tmp/$archive" "$base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"
want=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
else got=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1); fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "checksum mismatch for $archive" >&2; exit 1; }
tar xzf "$tmp/$archive" -C "$tmp"

dir="${LECTERN_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ] || command -v sudo >/dev/null 2>&1; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir" 2>/dev/null || true
if [ -w "$dir" ]; then
  install -m 755 "$tmp/$name" "$dir/$name"
else
  echo "Installing to $dir needs sudo."
  sudo install -m 755 "$tmp/$name" "$dir/$name"
fi
echo "Installed $("$dir/$name" version | head -1) to $dir/$name"
case ":$PATH:" in *":$dir:"*) ;; *) echo "Note: $dir is not on your PATH." ;; esac
if ! command -v tmux >/dev/null 2>&1; then
  echo "Note: running agents on this machine needs tmux and git (the CLI against a remote Lectern does not)."
fi
echo "Next: '$name serve' starts the control plane at http://localhost:9110; see https://github.com/$repo#quick-start"
