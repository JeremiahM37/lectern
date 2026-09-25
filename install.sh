#!/bin/sh
# Install the latest Lectern release for this machine.
#
#   curl -fsSL https://raw.githubusercontent.com/JeremiahM37/lectern/main/install.sh | sh
#
# Picks the archive for this OS/CPU from the latest GitHub release, verifies
# it against the release's checksums.txt, and installs `lectern` into
# ~/.local/bin (LECTERN_INSTALL_DIR overrides — set it to /usr/local/bin
# yourself if you want it system-wide; sudo only runs if that dir needs it).
# Set LECTERN_VERSION=vX.Y.Z to pin a release. Safe to re-run: it always
# overwrites with the requested version and touches nothing else.
#
# LECTERN_RELEASE_BASE overrides where archives/checksums are fetched from
# (tests point this at a local fixture server); leave it unset otherwise.
set -eu

repo="JeremiahM37/lectern"
name="lectern"
version="${LECTERN_VERSION:-latest}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  mingw*|msys*|cygwin*)
    echo "On Windows use PowerShell: irm https://raw.githubusercontent.com/$repo/main/install.ps1 | iex" >&2
    exit 1
    ;;
  *) echo "unsupported OS: $os" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported CPU: $(uname -m)" >&2; exit 1 ;;
esac

if [ -n "${LECTERN_RELEASE_BASE:-}" ]; then
  base="$LECTERN_RELEASE_BASE"
elif [ "$version" = latest ]; then
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
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  echo "checksum mismatch for $archive" >&2
  exit 1
fi
tar xzf "$tmp/$archive" -C "$tmp"

dir="${LECTERN_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$dir"
if [ -w "$dir" ]; then
  install -m 755 "$tmp/$name" "$dir/.$name.next"
  mv -f "$dir/.$name.next" "$dir/$name"
else
  echo "Installing to $dir needs sudo."
  sudo install -m 755 "$tmp/$name" "$dir/$name"
fi
echo "Installed $("$dir/$name" version | head -1) to $dir/$name"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Note: $dir is not on your PATH. Add: export PATH=\"$dir:\$PATH\"" ;;
esac

# distro_install NAME prints the exact command for whatever package manager
# this machine has, so a missing prerequisite is one copy-paste away instead
# of a trip to search engine + docs.
distro_install() {
  pkg="$1"
  if [ "$pkg" = python3 ] && { [ "$os" = darwin ] || command -v pacman >/dev/null 2>&1; }; then pkg=python; fi
  if [ "$os" = darwin ]; then
    if command -v brew >/dev/null 2>&1; then echo "  brew install $pkg"
    else echo "  install Homebrew (https://brew.sh), then: brew install $pkg"; fi
    return
  fi
  if command -v apt-get >/dev/null 2>&1; then echo "  sudo apt-get update && sudo apt-get install -y $pkg"
  elif command -v dnf >/dev/null 2>&1; then echo "  sudo dnf install -y $pkg"
  elif command -v yum >/dev/null 2>&1; then echo "  sudo yum install -y $pkg"
  elif command -v pacman >/dev/null 2>&1; then echo "  sudo pacman -S --noconfirm $pkg"
  elif command -v zypper >/dev/null 2>&1; then echo "  sudo zypper install -y $pkg"
  elif command -v apk >/dev/null 2>&1; then echo "  sudo apk add $pkg"
  else echo "  install $pkg with your system's package manager"; fi
}

missing=""
if ! command -v tmux >/dev/null 2>&1; then
  echo ""
  echo "tmux is missing — needed on any machine that runs agents directly (this one, or a remote target reached over SSH):"
  distro_install tmux
  missing="$missing tmux"
fi
if ! command -v git >/dev/null 2>&1; then
  echo ""
  echo "git is missing — every project and task worktree needs it:"
  distro_install git
  missing="$missing git"
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is missing — terminal and session helpers need it:"
  distro_install python3
  missing="$missing python3"
fi

echo ""
if [ -n "$missing" ]; then
  echo "Next: install the missing tool(s) above (${missing# }), then run '$dir/$name up'."
else
  echo "Next: run '$dir/$name up' — it starts Lectern, opens your browser, and gets you to a first session."
fi
