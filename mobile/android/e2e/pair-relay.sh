#!/bin/bash
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
W=${LECTERN_ANDROID_WORK:?set LECTERN_ANDROID_WORK to a scratch directory}
cd "$W"
export PATH=$PATH:~/android-sdk/platform-tools ANDROID_SERIAL=${ANDROID_SERIAL:-emulator-5584}
PKG=${PKG:-io.github.jeremiahm37.lectern.debug}
T=$(cat stack/owner-token)
F=$(curl -s -X POST -H "Authorization: Bearer $T" http://127.0.0.1:19210/api/relay/pair | python3 -c "import json,sys;print(json.load(sys.stdin)['fragment'])")
adb shell monkey -p $PKG -c android.intent.category.LAUNCHER 1 >/dev/null 2>&1
"$HERE/ui.py" tap "id/link$" 15
adb shell input text "'http://lectern.example/relay-pair#p=$F'"
"$HERE/ui.py" tap "id/connect$"
sleep 4
adb exec-out screencap -p > shot-01-pairing.png
adb shell input tap 540 1596
