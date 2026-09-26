"""Playwright end-to-end: a real browser against a real lectern binary.

The server runs in mock mode, so every flow here is the genuine one — real HTTP,
real SSE, real approval round trips — with only the target scripted.
"""
import os
from functools import cache
import shutil
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import pytest
from playwright.sync_api import sync_playwright

ROOT = Path(__file__).resolve().parents[1]
def _unused_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


PORT = _unused_port()
AUTH_PORT = _unused_port()
while AUTH_PORT == PORT:
    AUTH_PORT = _unused_port()
BASE = f"http://127.0.0.1:{PORT}"
AUTH_BASE = f"http://127.0.0.1:{AUTH_PORT}"
_BUILD_DIR = tempfile.TemporaryDirectory(prefix="lec-e2e-build-")

PHONE = {"width": 390, "height": 844}
DESKTOP = {"width": 1440, "height": 900}


def _port_open(port: int) -> bool:
    with socket.socket() as s:
        return s.connect_ex(("127.0.0.1", port)) == 0


@cache
def _binary() -> str:
    """The lectern binary under test — prebuilt via LECTERN_BIN, or built now."""
    if env := os.environ.get("LECTERN_BIN"):
        return env
    out = Path(_BUILD_DIR.name) / "lectern"
    go = shutil.which("go") or "/usr/local/go/bin/go"
    subprocess.run([go, "build", "-o", str(out), "./cmd/lectern"],
                   cwd=ROOT, check=True)
    return str(out)


# A developer often runs this suite from inside a Lectern session, whose
# environment names the real memory provider, carries its token, and says which
# live session and recovery checkpoint it belongs to. Inherited, every project a
# test created was provisioned a real note in the developer's own vault — eighteen
# of them in one run, in a vault that syncs to a phone. A fixture server talks to
# nothing outside itself unless a test says so.
OUTSIDE_WORLD = {"LECTERN_GRIMOIRE_URL": "", "LECTERN_GRIMOIRE_TOKEN": "",
                 "LECTERN_CHECKPOINT": "", "LECTERN_API": "",
                 "LECTERN_SESSION_ID": "", "GRIMOIRE_SESSION": "",
                 "LECTERN_MEDIA_DIR": "", "LECTERN_SCRATCH_TRASH": "", "LECTERN_LIVE": ""}

# Not every server in this suite starts through a fixture: the local-runtime
# tests launch their own from whatever environment the process has. So the
# process itself forgets the outside world, once, before anything is spawned.
for _name in [*OUTSIDE_WORLD, "LECTERN_BASE_URL", "LECTERN_DB",
              "LECTERN_GRIMOIRE_CONTEXT_MODE", "LECTERN_GRIMOIRE_CONTEXT_PROJECTS"]:
    os.environ.pop(_name, None)

# A shell starts background jobs (`cmd &`) with SIGINT and SIGQUIT ignored,
# and an ignored signal survives every exec after it. Tests that press Ctrl-C
# in a child's PTY then wait forever for a process that never saw it. Put the
# ordinary dispositions back, so the suite behaves the same however it was
# launched; children then inherit the defaults.
import signal as _signal
if _signal.getsignal(_signal.SIGINT) is _signal.SIG_IGN:
    _signal.signal(_signal.SIGINT, _signal.default_int_handler)
if _signal.getsignal(_signal.SIGQUIT) is _signal.SIG_IGN:
    _signal.signal(_signal.SIGQUIT, _signal.SIG_DFL)

# Parallel workers (pytest-xdist) share the namespace's /tmp and HOME. Many
# tests drive the default tmux server by fixed session names ("terminal-test",
# "terminal-two") and set global tmux options, so each worker gets its own tmux
# directory and home before any test runs. A worker then behaves exactly like
# a serial run that happens to see only its share of the tests.
_WORKER = os.environ.get("PYTEST_XDIST_WORKER", "")
if _WORKER:
    _tmux_root = Path(os.environ.get("ADK_TEST_TMUX_ROOT") or tempfile.gettempdir()) / f"worker-{_WORKER}"
    _tmux_root.mkdir(parents=True, exist_ok=True)
    os.environ["ADK_TEST_TMUX_ROOT"] = str(_tmux_root)
    os.environ["TMUX_TMPDIR"] = str(_tmux_root)
    os.environ.pop("ADK_TEST_TMUX_SOCKET", None)
    _home = Path(tempfile.gettempdir()) / f"lec-e2e-home-{_WORKER}"
    _home.mkdir(parents=True, exist_ok=True)
    os.environ["HOME"] = str(_home)


def _start(port: int, extra_env: dict):
    tmp = tempfile.mkdtemp(prefix="lec-e2e-")
    # Do not pass a caller's tmux client/server identity into fixture
    # subprocesses.  Each run gets private HOME and tmux state; the isolated
    # runner adds a mount/PID namespace around this as a second guard.
    private_home = Path(tmp) / "home"
    private_home.mkdir()
    private_tmux = Path(tmp) / "tmux"
    private_tmux.mkdir()
    env = {**os.environ,
           "LECTERN_MOCK": "1", "LECTERN_TICK": "0.1",
           "LECTERN_MOCK_DELAY": "0.25", "LECTERN_PORT": str(port),
           "LECTERN_HANDOFF_POLL": "0.1",
           "LECTERN_DB": str(Path(tmp) / "e2e.db"),
           "LECTERN_BASE_URL": f"http://127.0.0.1:{port}",
           "HOME": str(private_home),
           "TMUX": "",
           "TMUX_TMPDIR": str(private_tmux),
           **OUTSIDE_WORLD,
           **extra_env}
    startup_log = Path(tmp) / "server.log"
    with startup_log.open("wb") as log:
        proc = subprocess.Popen([_binary()], cwd=ROOT, env=env,
                                stdout=log, stderr=subprocess.STDOUT)
    for _ in range(100):
        if proc.poll() is not None:
            raise RuntimeError(f"server exited with {proc.returncode} before listening on {port}:\n{startup_log.read_text(errors='replace')[-16000:]}")
        if _port_open(port):
            break
        time.sleep(0.1)
    else:
        proc.kill()
        proc.wait()
        raise RuntimeError(f"server did not start on {port}")
    return proc


def _stop(proc, port: int):
    proc.terminate()
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()


@pytest.fixture(scope="session")
def server():
    proc = _start(PORT, {})
    try:
        yield BASE
    finally:
        _stop(proc, PORT)


@pytest.fixture(scope="session")
def auth_server():
    """A second server with a bearer token set, to prove the PWA works in token
    mode — fetch AND EventSource both have to thread the token through."""
    proc = _start(AUTH_PORT, {"LECTERN_AUTH_TOKEN": "secret123"})
    try:
        yield AUTH_BASE
    finally:
        _stop(proc, AUTH_PORT)


@pytest.fixture(scope="session")
def browser():
    with sync_playwright() as p:
        b = p.chromium.launch()
        yield b
        b.close()


@pytest.fixture(autouse=True)
def clean_board(server):
    """Clear the shared board before each test.

    Without this the session-scoped server accumulates tasks and every
    board-state-dependent test (deck panes past the 16 cap, filters, counts) gets
    slower and flakier as more tests are added.

    Sessions are RELEASED, never killed: the same asymmetry the product enforces,
    so the fixture cannot quietly destroy the scripted terminals other tests then
    expect to discover.
    """
    import json
    import urllib.request

    def wipe(kind):
        try:
            rows = json.load(urllib.request.urlopen(f"{server}/api/{kind}", timeout=10))
        except Exception:
            return
        for row in rows:
            try:
                urllib.request.urlopen(urllib.request.Request(
                    f"{server}/api/{kind}/{row['id']}", method="DELETE"), timeout=10)
            except Exception:
                pass

    wipe("tasks")
    wipe("sessions")
    yield


@pytest.fixture()
def page(browser, server, request):
    viewport = getattr(request, "param", DESKTOP)
    ctx = browser.new_context(viewport=viewport)
    pg = ctx.new_page()
    yield pg
    ctx.close()
