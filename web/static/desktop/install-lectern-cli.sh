#!/usr/bin/env bash
# Install the Linux client from a server you already trust through SSH.
set -euo pipefail
server=lectern
api=http://localhost:9110
while (($#)); do
  case "$1" in
    --server) server=${2:?Missing SSH alias}; shift 2;;
    --api) api=${2:?Missing API URL}; shift 2;;
    --help|-h) echo 'Usage: bash install-lectern-cli.sh --server SSH_ALIAS --api https://SERVER:PORT'; exit 0;;
    *) echo "Unknown option: $1" >&2; exit 2;;
  esac
done
[[ $server =~ ^[a-zA-Z0-9_@.:-]+$ && $server != -* ]] || { echo 'Invalid SSH alias' >&2; exit 2; }
[[ $api == http://* || $api == https://* ]] || { echo 'API must be an http(s) URL' >&2; exit 2; }
[[ $(uname -s) == Linux ]] || { echo 'This installer is for Linux. Use install-lectern-cli.ps1 on Windows.' >&2; exit 1; }
command -v ssh >/dev/null
command -v scp >/dev/null
remote_platform=$(ssh -n "$server" 'uname -s; uname -m')
[[ "$remote_platform" == "$(uname -s)"$'\n'"$(uname -m)" ]] || { echo 'Server/client architectures differ. Build lectern for your client architecture first.' >&2; exit 1; }
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
scp -q "$server:/usr/local/bin/lectern" "$stage/client"
chmod 700 "$stage/client"
"$stage/client" console --help >/dev/null
mkdir -p "$HOME/.local/bin" "$HOME/.local/lib/lectern"
# Preserve the previous client and launcher for rollback.
backup="$HOME/.local/state/lectern/cli-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$backup"
for file in "$HOME/.local/bin/lectern" "$HOME/.local/lib/lectern/client"; do
  [[ ! -f $file ]] || cp -p "$file" "$backup/$(basename "$file")"
done
install -m 755 "$stage/client" "$HOME/.local/lib/lectern/client.next"
mv "$HOME/.local/lib/lectern/client.next" "$HOME/.local/lib/lectern/client"
{
  echo '#!/usr/bin/env bash'
  printf 'export LECTERN_API=${LECTERN_API:-%q}\n' "$api"
  printf 'export LECTERN_ATTACH_HOST=${LECTERN_ATTACH_HOST:-%q}\n' "$server"
  echo 'if (($# == 0)); then set -- console; fi'
  printf 'exec %q "$@"\n' "$HOME/.local/lib/lectern/client"
} > "$stage/launcher"
install -m 755 "$stage/launcher" "$HOME/.local/bin/lectern"
echo 'Installed. Run lectern for the console, or lectern --help for commands.'
case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) echo 'Add ~/.local/bin to your shell PATH.';; esac
