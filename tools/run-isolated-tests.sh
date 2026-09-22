#!/usr/bin/env bash
# Run Lectern tests in a copied, unprivileged bubblewrap namespace.
#
# This is intentionally fail-closed.  The operator must review this runner
# and set ADK_ISOLATION_REVIEWED=1 before any test process can start.
set -euo pipefail

if [[ ${ADK_ISOLATION_REVIEWED:-} != 1 ]]; then
  echo "refusing to run: set ADK_ISOLATION_REVIEWED=1 after reviewing this runner" >&2
  exit 77
fi

source_dir=$(readlink -f "${1:-$(dirname "${BASH_SOURCE[0]}")/..}")
[[ -d "$source_dir" && -f "$source_dir/go.mod" ]] || {
  echo "source must be a Lectern checkout: $source_dir" >&2
  exit 2
}

mode=${ADK_TEST_MODE:-all}
case "$mode" in go|e2e|frontend|all|smoke) ;; *) echo "ADK_TEST_MODE must be go, e2e, frontend, all, or smoke" >&2; exit 2 ;; esac

command -v bwrap >/dev/null || { echo "bwrap is required" >&2; exit 2; }
command -v go >/dev/null || { echo "go is required" >&2; exit 2; }
ttyd=$(command -v ttyd || true)
if [[ $mode == e2e || $mode == all ]] && [[ -z $ttyd ]]; then
  echo "ttyd is required for isolated e2e mode" >&2
  exit 2
fi
node_bin=$(command -v node || true)
npm_bin=$(command -v npm || true)
if [[ $mode == frontend || $mode == all || $mode == e2e ]]; then
  [[ -n "$node_bin" && -n "$npm_bin" ]] || {
    echo "node and npm are required for isolated frontend mode" >&2
    exit 2
  }
fi

# Some non-login shells omit this variable even though the per-user bus is
# available. Only adopt the conventional runtime directory when it is owned by
# the invoking UID and contains its bus socket; never point systemd-run at a
# directory belonging to another user.
if [[ -z ${XDG_RUNTIME_DIR:-} ]]; then
  candidate_runtime=/run/user/$(id -u)
  if [[ -d "$candidate_runtime" && "$(stat -c %u "$candidate_runtime" 2>/dev/null)" == "$(id -u)" && -S "$candidate_runtime/bus" ]]; then
    export XDG_RUNTIME_DIR="$candidate_runtime"
  fi
fi

# Stage only the checkout under test.  The private copy is the only writable
# source mount below, so a faulty test cannot edit the canonical checkout or
# any live service files.
stage=$(mktemp -d /tmp/lec-isolated-stage.XXXXXX)
cleanup_stage() { find "$stage" -depth -delete 2>/dev/null || true; }
trap cleanup_stage EXIT
mkdir -p "$stage/src"
frontend_deps=
if [[ $mode == frontend || $mode == all || $mode == e2e ]]; then
  # Keep dependencies outside the writable checkout copy. A checkout-local
  # install is preferred; the canonical checkout is an explicitly read-only
  # fallback for local verification. Both package manifests must match so a
  # stale dependency tree cannot silently satisfy a different candidate.
  candidate_deps="$source_dir/frontend/node_modules"
  canonical_deps=${ADK_FRONTEND_DEPS:-/home/admin/projects/lectern/frontend/node_modules}
  if [[ -d "$candidate_deps" && -f "$candidate_deps/react/package.json" ]]; then
    frontend_deps=$(readlink -f "$candidate_deps")
  elif [[ -d "$canonical_deps" && -f "$canonical_deps/react/package.json" ]]; then
    candidate_lock=$(sha256sum "$source_dir/frontend/package-lock.json" | awk '{print $1}')
    candidate_package=$(sha256sum "$source_dir/frontend/package.json" | awk '{print $1}')
    canonical_root=$(readlink -f "$canonical_deps/..")
    canonical_lock=$(sha256sum "$canonical_root/package-lock.json" | awk '{print $1}')
    canonical_package=$(sha256sum "$canonical_root/package.json" | awk '{print $1}')
    if [[ $candidate_lock == "$canonical_lock" && $candidate_package == "$canonical_package" ]]; then
      frontend_deps=$(readlink -f "$canonical_deps")
    fi
  fi
  if [[ -z "$frontend_deps" ]]; then
    echo "frontend/node_modules is absent or does not match the package lock" >&2
    exit 2
  fi
  frontend_root=$(readlink -f "$frontend_deps/..")
  (cd "$frontend_root" && npm ls --all --json >/dev/null) || {
    echo "frontend dependency tree is not a clean npm install" >&2
    exit 2
  }
fi
# The build must produce dist from source in the namespace; never let a stale
# checkout-local dist make a failed or skipped build look healthy.
tar --exclude=.git --exclude=frontend/node_modules --exclude=frontend/dist -C "$source_dir" -cf - . | tar -C "$stage/src" -xf -
chmod -R a+rwX "$stage/src"
mkdir -p "$stage/bin" "$stage/home" "$stage/go" "$stage/cache" "$stage/state"
mkdir -p "$stage/vite-cache" "$stage/vite-temp"

tmux_root=/tmp/lec-test-tmux
host_tmux_socket=${TMUX-}
host_tmux_socket=${host_tmux_socket%%,*}
[[ -n "$host_tmux_socket" ]] || host_tmux_socket="/tmp/tmux-$(id -u)/default"

gomodcache=$(go env GOMODCACHE)
goroot=$(go env GOROOT)
venv=${ADK_PYTHON_VENV:-/home/admin/projects/lectern/.venv}
python_base=${LEC_PYTHON_BASE:-}
playwright=${ADK_PLAYWRIGHT_CACHE:-/home/admin/.cache/ms-playwright}
[[ -d "$venv" ]] || venv=
[[ -d "$playwright" ]] || playwright=

node_root=
if [[ -n "$node_bin" ]]; then
  node_real=$(readlink -f "$node_bin")
  # /usr is already mounted read-only. Hosted runners commonly install Node
  # below /opt/hostedtoolcache, which needs its own bind mount.
  if [[ "$node_real" != /usr/* ]]; then
    node_root=$(dirname "$(dirname "$node_real")")
  fi
fi

# bwrap's user namespace maps the command to a distinct nobody UID inside the
# sandbox while retaining no host privilege.  PID/network namespaces keep
# fixture processes and loopback HTTP on their own; only read-only toolchain,
# dependency, browser, and source mounts cross the boundary.  /tmp, /run, HOME,
# caches, DBs, and test workspaces are fresh per run.
 bwrap_args=(
  --die-with-parent --new-session --unshare-user --unshare-pid --unshare-net
  --unshare-ipc --unshare-uts --uid 65534 --gid 65534
  --bind "$stage/src" /src
  --ro-bind /usr /usr
  --ro-bind /bin /bin
  --ro-bind /lib /lib
  --ro-bind /lib64 /lib64
  --ro-bind /etc /etc
  --dev /dev --proc /proc --tmpfs /tmp --dir /tmp/home --dir /tmp/cache --dir /tmp/state --tmpfs /run
  --dir /go --dir /go/pkg --dir /home --dir /home/test
  --dir /opt --dir /opt/test-bin
  --chdir /src
)

# A CI-created venv may point at a hosted-toolcache Python installation whose
# absolute path is recorded in pyvenv.cfg. Mount that base installation at the
# same path so the venv keeps its interpreter and standard library intact.
if [[ -n "$python_base" ]]; then
  python_base=$(readlink -f "$python_base")
  [[ -d "$python_base" ]] || { echo "configured Python base is absent: $python_base" >&2; exit 2; }
  base_path=${python_base#/}
  base_parent=
  IFS=/ read -r -a base_parts <<<"$base_path"
  for base_part in "${base_parts[@]}"; do
    [[ -z "$base_part" ]] && continue
    base_parent+="/$base_part"
    [[ "$base_parent" == "$python_base" ]] && break
    bwrap_args+=(--dir "$base_parent")
  done
  bwrap_args+=(--ro-bind "$python_base" "$python_base")
fi

bwrap_args+=(--ro-bind "$goroot" /usr/local/go)
bwrap_args+=(--ro-bind "$gomodcache" /go/pkg/mod)
if [[ -n "$node_root" && "$node_root" != /usr ]]; then
  bwrap_args+=(--dir /opt/node --ro-bind "$node_root" /opt/node)
fi
if [[ -n "$frontend_deps" ]]; then
  bwrap_args+=(--ro-bind "$frontend_deps" /src/frontend/node_modules)
  # Vite's dev server optimises dependencies into node_modules/.vite. Keep
  # that cache writable without making the dependency tree writable.
  bwrap_args+=(--bind "$stage/vite-cache" /src/frontend/node_modules/.vite)
  # Vite bundles its TypeScript config through a short-lived file below
  # node_modules/.vite-temp. Keep that scratch directory private and writable
  # while the dependency tree itself remains read-only.
  bwrap_args+=(--bind "$stage/vite-temp" /src/frontend/node_modules/.vite-temp)
fi
if [[ -n "$venv" ]]; then
  bwrap_args+=(--dir /opt/venv --ro-bind "$venv" /opt/venv)
fi
if [[ -n "$playwright" ]]; then
  bwrap_args+=(--dir /opt/playwright --ro-bind "$playwright" /opt/playwright)
fi
bwrap_args+=(--ro-bind "$stage/bin/tmux" /opt/test-bin/tmux)
if [[ -n "$ttyd" ]]; then
  # e2e's real browser fixture needs ttyd; expose only this executable rather
  # than the host's /usr/local tree, and keep it read-only in the namespace.
  bwrap_args+=(--ro-bind "$ttyd" /opt/test-bin/ttyd)
fi

# The source copy is generated above, so put the wrapper into it without
# writing the source checkout.  It is mounted read-only in the namespace.
cp "$source_dir/tools/tmux-isolated-wrapper" "$stage/bin/tmux"
chmod 0755 "$stage/bin/tmux"

inner=(/bin/bash -c)
go_test_cmd='GOMAXPROCS=2 go test ./... -count=1'
if [[ -n ${ADK_TEST_GO_ARGS:-} ]]; then
  # Parse the optional focused-test arguments as shell words here, then quote
  # each word before placing it in the namespace command. This keeps the
  # convenience filter from becoming shell syntax while still allowing normal
  # `go test` flags such as -run and -count.
  read -r -a go_test_args <<<"$ADK_TEST_GO_ARGS"
  for arg in "${go_test_args[@]}"; do
    printf -v quoted ' %q' "$arg"
    go_test_cmd+="$quoted"
  done
fi
e2e_test_cmd='/opt/venv/bin/python -m pytest -q e2e --durations=20'
if [[ -n ${ADK_TEST_E2E_ARGS:-} ]]; then
  read -r -a e2e_args <<<"$ADK_TEST_E2E_ARGS"
  e2e_test_cmd='/opt/venv/bin/python -m pytest'
  for arg in "${e2e_args[@]}"; do
    printf -v quoted ' %q' "$arg"
    e2e_test_cmd+="$quoted"
  done
fi
frontend_test_cmd='set -euo pipefail
cd /src/frontend
npm test
npm run build
for pair in "index.html /src/web/index.html" "terminal.html /src/web/static/terminal.html" "sw.js /src/web/static/sw.js"; do
  set -- $pair
  cmp -s "/src/frontend/dist/$1" "$2" || { echo "frontend artifact mismatch: $1 vs $2" >&2; exit 1; }
done
while IFS= read -r -d "" asset; do
  relative=${asset#/src/frontend/dist/assets/}
  cmp -s "$asset" "/src/web/static/react/assets/$relative" || {
    echo "frontend artifact mismatch: assets/$relative" >&2
    exit 1
  }
done < <(find /src/frontend/dist/assets -type f -print0)
echo "PASS: frontend tests, build, and staged artifact comparison"'
case "$mode" in
  smoke)
    inner+=(
      'set -euo pipefail
       test "${ADK_TEST_ISOLATED}" = 1
       test "$(id -u)" = 65534
       test "${TMUX:-}" = ""
       test "${HOME}" = /tmp/home
       test ! -e "${ADK_HOST_TMUX_SOCKET}"
       mkdir -p "$ADK_TEST_TMUX_ROOT"
       tmux -f /dev/null new-session -d -s lec-isolation-smoke -- sleep 30
       tmux has-session -t =lec-isolation-smoke
       mkdir -p /tmp/fixture-root
       tmux -f /dev/null new-session -d -s terminal-test -c /tmp/fixture-root bash --norc
       tmux has-session -t =terminal-test
       tmux kill-session -t =terminal-test
       tmux kill-session -t =lec-isolation-smoke
       ! tmux has-session -t =lec-isolation-smoke 2>/dev/null
       tmux -L lec-label-a -f /dev/null new-session -d -s label-a -- sleep 30
       tmux -L lec-label-b -f /dev/null new-session -d -s label-b -- sleep 30
       tmux -L lec-label-a has-session -t =label-a
       tmux -L lec-label-b has-session -t =label-b
       tmux -L lec-label-a kill-session -t =label-a
       tmux -L lec-label-b kill-session -t =label-b
       ! tmux -L lec-label-a has-session -t =label-a 2>/dev/null
       ! tmux -L lec-label-b has-session -t =label-b 2>/dev/null
       echo "PASS: isolated tmux smoke"'
    )
    ;;
  go) inner+=("$go_test_cmd") ;;
  e2e) inner+=("[[ -x /opt/venv/bin/python ]] || { echo \"Python venv is required for e2e mode\" >&2; exit 2; }; $e2e_test_cmd") ;;
  frontend) inner+=("$frontend_test_cmd") ;;
  all) inner+=("set -e; $go_test_cmd; $frontend_test_cmd; cd /src; [[ -x /opt/venv/bin/python ]] || { echo \"Python venv is required for e2e mode\" >&2; exit 2; }; $e2e_test_cmd") ;;
esac

env_args=(
  --clearenv
  --setenv PATH /opt/test-bin:/opt/venv/bin:/opt/node/bin:/usr/local/go/bin:/usr/bin:/bin
  --setenv HOME /tmp/home
  # The namespace UID is nobody (65534), whose passwd shell is nologin on
  # Debian.  tmux uses SHELL when it creates panes; tests must exercise the
  # command under a usable headless shell without changing production launch
  # behavior.
  --setenv SHELL /bin/bash
  --setenv USER nobody
  --setenv LOGNAME nobody
  --setenv XDG_CONFIG_HOME /tmp/home/.config
  --setenv XDG_CACHE_HOME /tmp/cache
  --setenv XDG_STATE_HOME /tmp/state
  --setenv GOCACHE /tmp/go-cache
  --setenv GOPATH /tmp/go
  --setenv GOMODCACHE /go/pkg/mod
  --setenv GOTOOLCHAIN local
  --setenv GOFLAGS -buildvcs=false
  --setenv PLAYWRIGHT_BROWSERS_PATH /opt/playwright
  --setenv TMUX ""
  --setenv TMUX_TMPDIR "$tmux_root"
  --setenv ADK_TEST_TMUX_ROOT "$tmux_root"
  --setenv ADK_TEST_ISOLATED 1
  --setenv ADK_HOST_TMUX_SOCKET "$host_tmux_socket"
)

# Resource bound: 8 GiB, 512 tasks, two host CPU cores.  The explicit
# GOMAXPROCS bound remains in Go modes even when a caller runs without cgroups.
# `--wait` is for transient service units and cannot be combined with a scope;
# scope invocations already remain attached until the command exits.
scope=(systemd-run --user --expand-environment=no --scope --collect
  -p MemoryMax=8G -p TasksMax=512 -p CPUQuota=200%)
if [[ ${ADK_TEST_NO_SCOPE:-0} == 1 && $mode != smoke ]]; then
  echo "ADK_TEST_NO_SCOPE=1 is permitted only for the smoke proof" >&2
  exit 2
fi
if [[ ${ADK_TEST_NO_SCOPE:-0} == 1 ]]; then
  scope=()
fi

"${scope[@]}" bwrap "${bwrap_args[@]}" "${env_args[@]}" -- "${inner[@]}"
