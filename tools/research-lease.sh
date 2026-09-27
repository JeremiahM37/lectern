#!/bin/sh
# Installation template for the existing inference-research/floor wrapper.
# Do not execute this checkout path as a substitute physical lease location.
set -eu
if [ "$#" -eq 0 ]; then
    echo 'usage: research-lease.sh SUPERVISOR [ARG ...]' >&2
    exit 64
fi
lease_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)/.machine-leases
# Preserve the existing lock inode. Registration must have established it.
[ -f "$lease_dir/host-lxc105.lock" ] || exit 75
exec flock --nonblock --conflict-exit-code 75 --no-fork \
    "$lease_dir/host-lxc105.lock" sh -c '
      sudo -n /usr/bin/python3 -I /usr/local/libexec/lectern-autonomy-gpu-lease.py check || exit "$?"
      exec "$@"
    ' research-lease "$@"
