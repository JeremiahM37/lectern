# First-install acceptance — 2026-09-25

Candidate coverage (isolated from operator sessions and credentials):

| Path | Evidence |
|---|---|
| Linux source and binary installs | `e2e/test_local_mode.py`: fresh homes, real tmux, session/task persistence and reattach |
| Background local runtime | supervisor adopts the existing PID, recovers a stopped engine with the same URL/database; systemd/launchd configuration tests |
| Remote client routing | `up` rejects an explicit remote API instead of silently opening another board |
| Fresh browser | `e2e/test_first_run.py`: phone and desktop, add a machine through Settings, then open the session dialog |
| Docker | real multi-stage image build; Python, authenticated health, fresh database, local shell input and persisted scratch files |
| Docker → SSH | disposable OpenSSH target and test key; toolchain probe, shell input, container recreation and preserved remote terminal |
| Browser → Docker → SSH | token sign-in, phone/desktop first-machine setup, actual WebSocket terminal typing after container recreation |
| Windows amd64 / macOS arm64 | cross-compiled successfully; no native Windows/macOS runtime acceptance in this run |

Repeat Docker acceptance on an unused Docker test host:

```sh
docker build -f deploy/Dockerfile -t lectern:acceptance .
python3 -m pip install playwright
python3 -m playwright install chromium
LECTERN_TEST_IMAGE=lectern:acceptance LECTERN_TEST_URL=http://127.0.0.1:19110 \
  python3 tools/test-container-install.py
```

The harness uses uniquely named containers, network and volume; it removes only
those resources. It installs OpenSSH in its disposable target. Its port 19110
is authenticated with a fresh test token. It exercises real shell/transport
behavior without calling a paid provider. The release workflow now gates both
binary and image publication on this acceptance job.

Run the project `verify` suite for Go, frontend, isolated browser/PTY tests and
live service/UI checks. Unit generation is not proof of macOS launchd behavior;
macOS runtime and physical-phone keyboard behavior remain separate checks.

The public v2.3.1 archive was downloaded and checked: `up` is absent. A new
approved release is required before public installers provide this workflow.
No public push, tag or release is part of this local acceptance run.
