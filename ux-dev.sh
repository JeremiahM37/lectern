#!/bin/bash
# UX dev loop: rebuild the web UI + binary and (re)start an ISOLATED mock Lectern on :$UX_PORT (default 9141).
# Mirrors e2e/conftest.py isolation: LECTERN_MOCK, private HOME/tmux/PTY socket/DB, no Grimoire.
# Never touches the live :9110 server, its DB, or the user's tmux sessions.
set -e
PORT=${UX_PORT:-9141}
cd "$(dirname "$0")"
export PATH=$PATH:/usr/local/go/bin
if [ "$1" != nobuild ]; then
  (npm run build --prefix frontend >/tmp/lux-build-$PORT.log 2>&1 && python3 frontend/scripts/stage.py >>/tmp/lux-build-$PORT.log 2>&1) || { tail -30 /tmp/lux-build-$PORT.log; exit 1; }
  go build -o /tmp/lux-bin-$PORT ./cmd/lectern
fi
[ -f /tmp/lux-$PORT.pid ] && kill $(cat /tmp/lux-$PORT.pid) 2>/dev/null || true
sleep 0.5
S=/tmp/lux-state-$PORT; mkdir -p $S/home $S/tmux
env -u LECTERN_GRIMOIRE_URL -u LECTERN_GRIMOIRE_TOKEN -u LECTERN_CHECKPOINT -u LECTERN_API -u LECTERN_SESSION_ID \
  -u GRIMOIRE_SESSION -u LECTERN_MEDIA_DIR -u LECTERN_LIVE -u TMUX \
  LECTERN_MOCK=1 LECTERN_TICK=0.5 LECTERN_MOCK_DELAY=0.25 LECTERN_PORT=$PORT LECTERN_DB=$S/ux.db \
  LECTERN_BASE_URL=http://127.0.0.1:$PORT HOME=$S/home TMUX_TMPDIR=$S/tmux \
  LECTERN_PTYHOST_SOCKET=$S/pty.sock LECTERN_PTYHOST_TEST_GUARD=1 \
  nohup /tmp/lux-bin-$PORT serve >/tmp/lux-server-$PORT.log 2>&1 &
echo $! >/tmp/lux-$PORT.pid
for i in $(seq 40); do curl -sf http://127.0.0.1:$PORT/ >/dev/null && echo "ready http://127.0.0.1:$PORT (state $S)" && exit 0; sleep 0.5; done
tail -20 /tmp/lux-server-$PORT.log; exit 1
