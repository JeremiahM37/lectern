#!/usr/bin/env bash
# Lectern scale benchmark. Builds this checkout and runs tools/stress inside the
# reviewed bubblewrap namespace (tools/run-isolated-tests.sh, mode "stress"), so
# the instance under test has its own port, database, HOME and tmux servers and
# cannot reach the live service or the host's tmux server.
#
#   ADK_ISOLATION_REVIEWED=1 tools/stress/run.sh [OUT_DIR] [driver flags...]
#
# e.g. tools/stress/run.sh /tmp/stress -tiers 10,25 -window 30s
# Results: OUT_DIR/results.json, OUT_DIR/results.md, OUT_DIR/lectern-N.log.
# ADK_TEST_CPUS caps the namespace's CPU (default 8 cores).
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
out=$(readlink -m "${1:-$here/stress-results}")
shift || true
export ADK_TEST_MODE=stress ADK_STRESS_OUT="$out" ADK_TEST_CPUS=${ADK_TEST_CPUS:-8}
export ADK_STRESS_ARGS="-cpu-cores $ADK_TEST_CPUS $*"
exec "$here/tools/run-isolated-tests.sh" "$here"
