#!/usr/bin/env bash
# Run the Go, frontend and browser suites at the same time, each in its own
# isolated namespace (tools/run-isolated-tests.sh), and report one result.
#
# The suites share nothing but the host, so running them one after another
# only added their times together. The CPU budget is split so the browser
# suite, which is by far the longest, gets most of it.
#
#   ADK_ISOLATION_REVIEWED=1 tools/test-all-parallel.sh [checkout]
#
# ADK_TEST_CPUS sets the total budget (default: half the host's cores).
# ADK_TEST_GOCACHE is passed through (see run-isolated-tests.sh).
set -uo pipefail
# Job control, so the suites started with `&` below keep ordinary SIGINT and
# SIGQUIT handling. Without it bash starts them with both ignored, every test
# process inherits that, and a test that presses Ctrl-C in a child's terminal
# waits forever.
set -m

source_dir=$(readlink -f "${1:-$(dirname "${BASH_SOURCE[0]}")/..}")
runner="$source_dir/tools/run-isolated-tests.sh"
host_cpus=$(nproc)
total=${ADK_TEST_CPUS:-$(( host_cpus / 2 > 6 ? host_cpus / 2 : 6 ))}
# Go time is dominated by one package that runs its tests serially, so more
# than four cores buys it almost nothing; the rest go to the browser workers.
go_cpus=$(( total / 4 > 4 ? 4 : (total / 4 > 2 ? total / 4 : 2) ))
frontend_cpus=2
e2e_cpus=$(( total - go_cpus - frontend_cpus ))
(( e2e_cpus >= 2 )) || e2e_cpus=2

logs=$(mktemp -d /tmp/lec-test-all.XXXXXX)
declare -A pids started
start_suite() {
  local mode=$1 cpus=$2
  started[$mode]=$(date +%s)
  ADK_TEST_MODE=$mode ADK_TEST_CPUS=$cpus "$runner" "$source_dir" >"$logs/$mode.log" 2>&1 &
  pids[$mode]=$!
}
# Job control puts each suite in its own process group, so an interrupt of
# this script would no longer reach them. Stop them explicitly instead.
stop_suites() {
  local pid
  for pid in "${pids[@]}"; do kill -TERM -- "-$pid" 2>/dev/null || true; done
}
trap 'stop_suites; exit 130' INT TERM
start_suite e2e "$e2e_cpus"
start_suite go "$go_cpus"
start_suite frontend "$frontend_cpus"
echo "running go ($go_cpus cpus), frontend ($frontend_cpus), e2e ($e2e_cpus); logs in $logs"

status=0
for mode in frontend go e2e; do
  wait "${pids[$mode]}"
  code=$?
  seconds=$(( $(date +%s) - ${started[$mode]} ))
  if (( code == 0 )); then
    echo "PASS $mode (${seconds}s)"
  else
    status=1
    echo "FAIL $mode (exit $code, ${seconds}s) -- last lines of $logs/$mode.log:"
    tail -n 60 "$logs/$mode.log" | sed 's/^/    /'
  fi
done
if (( status == 0 )); then
  echo "PASS: all suites"
  # The slowest browser tests are the ones that set the floor for the run.
  sed -n '/slowest/,/^$/p' "$logs/e2e.log" | head -12
else
  echo "FAILED: see logs in $logs"
fi
exit "$status"
