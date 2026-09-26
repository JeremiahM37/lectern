#!/bin/bash
# Stand-in for the Claude Code CLI in an interactive Lectern session. The driver
# writes this file once per run with STRESS_LOG_DIR and STRESS_* filled in.
#
# It does what a working agent does to Lectern and nothing an LLM does: it
# writes to its terminal, edits files in its repository (reported through the
# PreToolUse/PostToolUse hooks Lectern installed through --settings, which is
# what feeds cross-agent awareness), and asks for permission through the
# PermissionRequest hook, blocking until the operator's decision comes back.
# Each approval request is logged with nanosecond wall-clock timestamps so the
# driver can compute the round trip.
log_dir=__STRESS_LOG_DIR__
interval=__STRESS_APPROVAL_EVERY__
settings=""
prev=""
for arg in "$@"; do
  [ "$prev" = --settings ] && settings=$arg
  prev=$arg
done
name=$(basename "${settings:-unknown.json}" .json)
log="$log_dir/$name.log"
hook_base=""
if [ -f "$settings" ]; then
  hook_base=$(python3 -c 'import json,sys; u=json.load(open(sys.argv[1]))["hooks"]["PreToolUse"][0]["hooks"][0]["url"]; print(u.rsplit("/",1)[0])' "$settings" 2>/dev/null)
fi
echo "start $(date +%s%N) $name $hook_base" >> "$log"

post() {  # post EVENT JSON MAXTIME -> prints body
  curl -s -m "$3" -X POST -H "Authorization: Bearer $LECTERN_HOOK_TOKEN" \
    -H 'Content-Type: application/json' --data-binary "$2" "$hook_base/$1"
}

printf '\033[1mClaude Code (stress stand-in)\033[0m  %s\n\n' "$name"
[ -n "$hook_base" ] && post SessionStart '{"hook_event_name":"SessionStart","source":"startup"}' 8 >/dev/null
# spread the first request across one interval so N agents do not all ask at once
sleep "$(awk -v s="$RANDOM" -v i="$interval" 'BEGIN{printf "%.2f", (s%1000)/1000*i}')"
seq=0
while true; do
  seq=$((seq+1))
  cmd="make test # stress $name $seq"
  printf '● Running step %d…\n' "$seq"
  if [ -n "$hook_base" ]; then
    tool="{\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"$cmd\"}}"
    post PreToolUse "$tool" 3 >/dev/null
    t0=$(date +%s%N)
    body=$(post PermissionRequest "$tool" 130)
    rc=$?
    t3=$(date +%s%N)
    decision=$(printf '%s' "$body" | python3 -c 'import json,sys
try: print(json.load(sys.stdin)["hookSpecificOutput"]["decision"]["behavior"])
except Exception: print("none")' 2>/dev/null)
    echo "approval $seq $t0 $t3 $rc $decision" >> "$log"
    post PostToolUse "$tool" 3 >/dev/null
    # one file edit per step; agents in the same repository share file names,
    # so their edits overlap the way parallel agents' edits do
    file="$PWD/src/module_$((seq % 5)).txt"
    mkdir -p "$PWD/src" && printf '%s %s\n' "$name" "$seq" >> "$file"
    edit="{\"tool_name\":\"Edit\",\"tool_input\":{\"file_path\":\"$file\",\"old_string\":\"a\",\"new_string\":\"b\"}}"
    post PreToolUse "$edit" 3 >/dev/null
    post PostToolUse "$edit" 3 >/dev/null
    post Stop '{"hook_event_name":"Stop"}' 8 >/dev/null
  fi
  printf '  ⎿ %s: %s\n\n❯ ' "$cmd" "${decision:-no hook}"
  sleep "$interval"
  printf '\n'
done
