#!/bin/bash
# A real approval → encrypted push via ntfy → notification → a button pressed
# with no Lectern screen open → the agent continues (or stops).
# usage: approve-flow.sh <label> [Approve|Deny|Reply] [reply text]
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
W=${LECTERN_ANDROID_WORK:?set LECTERN_ANDROID_WORK to a scratch directory}
cd "$W"
export PATH=$PATH:~/android-sdk/platform-tools ANDROID_SERIAL=${ANDROID_SERIAL:-emulator-5584}
PKG=${PKG:-io.github.jeremiahm37.lectern.debug}
TAG=${1:-relay}; BUTTON=${2:-Approve}; REPLY=${3:-}
H="Authorization: Bearer $(cat stack/owner-token)"
API=http://127.0.0.1:19210/api
adb shell cmd statusbar collapse
adb shell input keyevent KEYCODE_HOME
sleep 1
adb shell am kill $PKG
sleep 1
echo "app process after kill: '$(adb shell pidof $PKG || true)'"
TITLE="Android $BUTTON $TAG $(date +%s)"
TID=$(curl -s -H "$H" -H 'Content-Type: application/json' -X POST $API/tasks -d "{\"project_id\":1,\"title\":\"$TITLE\",\"prompt\":\"deploy [mock:approval]\",\"permission_mode\":\"default\"}" | python3 -c "import json,sys;print(json.load(sys.stdin)['id'])")
curl -s -H "$H" -H 'Content-Type: application/json' -X POST $API/tasks/$TID/dispatch -d '{}' >/dev/null
for i in $(seq 30); do
  AID=$(sqlite3 stack/lectern.db "select a.id from approvals a join attempts t on t.id=a.attempt_id where t.task_id=$TID")
  [ -n "$AID" ] && break; sleep 1
done
echo "task $TID waiting on approval $AID, status=$(curl -s -H "$H" $API/tasks/$TID | python3 -c "import json,sys;print(json.load(sys.stdin)['status'])")"
adb shell cmd statusbar expand-notifications
"$HERE/ui.py" wait "^$BUTTON\$" 30 >/dev/null
sleep 1
adb exec-out screencap -p > shot-03-notification-$TAG.png
echo "foreground activity: $(adb shell dumpsys activity activities | grep -m1 topResumedActivity | sed "s/.*u0 //;s/ t[0-9]*}//")"
"$HERE/ui.py" tap "^$BUTTON\$" >/dev/null
if [ "$BUTTON" = Reply ]; then
  sleep 1
  adb shell input text "'$REPLY'"
  "$HERE/ui.py" tap "Send" >/dev/null
fi
for i in $(seq 40); do
  ST=$(sqlite3 stack/lectern.db "select status from approvals where id=$AID")
  [ -n "$ST" ] && [ "$ST" != pending ] && break
  sleep 1
done
sqlite3 stack/lectern.db "select 'approval', id, status, decided_by, coalesce(note,'') from approvals where id=$AID"
sleep 4
echo "task status: $(curl -s -H "$H" $API/tasks/$TID | python3 -c "import json,sys;print(json.load(sys.stdin)['status'])")"
curl -s -H "$H" $API/tasks/$TID/events | python3 -c "import json,sys;print('last agent text:', [e['payload'].get('text') for e in json.load(sys.stdin) if e['type']=='text'][-1])"
adb shell cmd statusbar expand-notifications
sleep 2
adb exec-out screencap -p > shot-04-result-$TAG.png
