#!/bin/bash
# Isolated Lectern (mock agents, token auth, device pairing) and a local
# `lectern relay` for the Android emulator test (docs/android.md). Run ntfy
# yourself on 127.0.0.1:19281 and `adb reverse` 19210, 19281 and 19282. Nothing here touches
# the live service: own port, DB, HOME and tmux directory.
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
W=${LECTERN_ANDROID_WORK:?set LECTERN_ANDROID_WORK to a scratch directory}
S=$W/stack
LECTERN_BIN=${LECTERN_BIN:?set LECTERN_BIN to a lectern binary}
mkdir -p $S/home $S/tmux
export LECTERN_RELAY_HOST_SECRET=$(cat $S/relay-secret 2>/dev/null || (openssl rand -hex 32 | tee $S/relay-secret))
nohup $LECTERN_BIN relay --listen 127.0.0.1:19282 > $S/relay.log 2>&1 &
echo $! > $S/relay.pid
env -i PATH=/usr/local/bin:/usr/bin:/bin HOME=$S/home TMUX_TMPDIR=$S/tmux \
  LECTERN_MOCK=1 LECTERN_TICK=0.1 LECTERN_MOCK_DELAY=0.25 LECTERN_HANDOFF_POLL=0.1 \
  LECTERN_PORT=19210 LECTERN_HOST=127.0.0.1 LECTERN_DB=$S/lectern.db \
  LECTERN_BASE_URL=http://127.0.0.1:19210 \
  LECTERN_AUTH=token LECTERN_AUTH_TOKEN=$(cat $S/owner-token 2>/dev/null || (openssl rand -hex 16 | tee $S/owner-token)) \
  LECTERN_DEVICE_PAIRING=1 \
  LECTERN_RELAY_URL=ws://127.0.0.1:19282 LECTERN_RELAY_HOST_SECRET=$LECTERN_RELAY_HOST_SECRET \
  nohup $LECTERN_BIN > $S/lectern.log 2>&1 &
echo $! > $S/lectern.pid
