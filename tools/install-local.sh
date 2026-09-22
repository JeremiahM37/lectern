#!/usr/bin/env bash
# Install the standalone local Lectern command.
#
# This uses a separate name only when the SSH client already owns `lectern`.
set -euo pipefail

source_dir=""
binary=""
prefix="${XDG_BIN_HOME:-${HOME:?HOME is required}/.local/bin}"
name=""

usage() {
  cat <<'EOF'
Usage: bash tools/install-local.sh [options]

Install the standalone local Lectern command from a checkout or binary.
The default command is `lectern`; if that name is already installed, the
installer uses `lectern-local` so the remote client keeps working.

Options:
  --source DIR    Build from this Lectern checkout
  --binary FILE   Install this already-built Lectern binary
  --prefix DIR    Install directory (default: ~/.local/bin)
  --name NAME     Command name (default: lectern, or lectern-local if busy)
  -h, --help      Show this help
EOF
}

while (($#)); do
  case "$1" in
    --source) source_dir=${2:?Missing source directory}; shift 2;;
    --binary) binary=${2:?Missing binary path}; shift 2;;
    --prefix) prefix=${2:?Missing install directory}; shift 2;;
    --name) name=${2:?Missing command name}; shift 2;;
    --help|-h) usage; exit 0;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 2;;
  esac
done

case "$(uname -s)" in
  Linux|Darwin) ;;
  MINGW*|MSYS*|CYGWIN*)
    echo 'Run this installer inside WSL2 (Linux), not from Windows Git Bash.' >&2
    echo 'The local runtime uses Linux tmux and a WSL-visible agent CLI.' >&2
    exit 1
    ;;
  *)
    echo "Unsupported host OS: $(uname -s). Use Linux, macOS, or WSL2." >&2
    exit 1
    ;;
esac

file_hash() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo 'A SHA-256 utility (sha256sum or shasum) is required.' >&2
    return 1
  fi
}

for prerequisite in git tmux python3; do
  command -v "$prerequisite" >/dev/null || {
    echo "Missing prerequisite: $prerequisite (install it before local Lectern)." >&2
    exit 1
  }
done

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
if [[ -z $source_dir && -z $binary && -f "$script_dir/../go.mod" ]]; then
  source_dir=$(cd -- "$script_dir/.." && pwd)
fi
if [[ -n $source_dir ]]; then
  source_dir=$(cd -- "$source_dir" && pwd)
  [[ -f "$source_dir/go.mod" ]] || { echo "Not a Lectern checkout: $source_dir" >&2; exit 2; }
fi
if [[ -n $binary ]]; then
  [[ -f $binary ]] || { echo "Binary not found: $binary" >&2; exit 2; }
  binary=$(cd -- "$(dirname -- "$binary")" && pwd)/$(basename -- "$binary")
fi
if [[ -n $source_dir && -n $binary ]]; then
  echo 'Choose --source or --binary, not both.' >&2
  exit 2
fi

stage=$(mktemp -d "${TMPDIR:-/tmp}/lectern-local.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT
candidate="$stage/lectern"

if [[ -n $binary ]]; then
  install -m 755 "$binary" "$candidate"
elif [[ -n $source_dir ]]; then
  command -v go >/dev/null || {
    echo 'Source build needs Go 1.25 or newer; install Go or pass --binary.' >&2
    exit 1
  }
  echo "Building Lectern from $source_dir"
  (cd -- "$source_dir" && go build -trimpath -o "$candidate" ./cmd/lectern)
else
  echo 'Provide --source PATH or --binary FILE; no release assets are configured.' >&2
  exit 1
fi

[[ -x $candidate ]] || { echo 'The candidate is not executable.' >&2; exit 1; }
if ! "$candidate" version >/dev/null 2>&1; then
  echo 'The candidate did not answer `lectern version`; refusing to install it.' >&2
  exit 1
fi
if ! "$candidate" local --help >/dev/null 2>&1; then
  echo 'The candidate must also support `lectern local --help`.' >&2
  exit 1
fi

mkdir -p -- "$prefix"
if [[ -z $name ]]; then
  name=lectern
  managed_marker="${XDG_STATE_HOME:-${HOME:?HOME is required}/.local/state}/lectern/local/installed/$name"
  managed=0
  if [[ -f "$prefix/$name" && -f "$managed_marker" ]]; then
    marker_path=$(sed -n '1p' "$managed_marker")
    marker_hash=$(sed -n '2p' "$managed_marker")
    [[ $marker_path == "$prefix/$name" && $marker_hash == "$(file_hash "$prefix/$name")" ]] && managed=1
  fi
  if [[ -e "$prefix/$name" || -L "$prefix/$name" ]] && (( ! managed )); then
    name=lectern-local
  fi
fi
[[ $name =~ ^[a-zA-Z0-9._+-]+$ ]] || { echo 'Invalid command name' >&2; exit 2; }
destination="$prefix/$name"
state_root="${XDG_STATE_HOME:-${HOME:?HOME is required}/.local/state}/lectern/local"
if [[ -e $destination || -L $destination ]]; then
  backup_dir="$state_root/backups/$(date +%Y%m%d-%H%M%S)"
  mkdir -p -- "$backup_dir"
  cp -p -- "$destination" "$backup_dir/$name"
  echo "Previous $name saved in $backup_dir/$name"
fi
staged_destination="$prefix/.${name}.install.$$"
install -m 755 "$candidate" "$staged_destination"
mv -f -- "$staged_destination" "$destination"
mkdir -p -- "$state_root/installed"
marker_tmp="$state_root/installed/.${name}.tmp.$$"
printf '%s\n%s\n' "$destination" "$(file_hash "$destination")" > "$marker_tmp"
mv -f -- "$marker_tmp" "$state_root/installed/$name"
echo "Installed $destination"
echo "Run: $name local"
case ":${PATH:-}:" in
  *":$prefix:"*) ;;
  *) echo "Add $prefix to PATH before using $name.";;
esac
