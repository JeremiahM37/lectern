"""`lectern claude` end to end: real CLI, real server, real (private) tmux —
only the agent binary is a stub, per the workspace's no-real-model-tokens rule.

Unlike real_terminal (test_terminal_workspace.py), this fixture starts a
completely fresh server with no session pre-adopted: the whole point of this
command is that it creates the tracked session itself, for whatever directory
the CLI happens to be run in.
"""
import json
import os
import pty
import select
import subprocess
import time
import urllib.request
from pathlib import Path

import pytest

from conftest import OUTSIDE_WORLD, _binary, _unused_port

STUB_AGENT = '''#!/bin/bash
echo "stub agent ready"
while IFS= read -r line; do :; done
'''


@pytest.fixture()
def one_command_server(tmp_path):
    agent = tmp_path / "stub-claude"
    agent.write_text(STUB_AGENT)
    agent.chmod(0o755)
    tmux_dir = tmp_path / "tmux"
    tmux_dir.mkdir()
    home = tmp_path / "home"
    home.mkdir()
    port = _unused_port()
    url = f"http://127.0.0.1:{port}"
    env = {
        **os.environ, **OUTSIDE_WORLD,
        "LECTERN_MOCK": "0", "LECTERN_DB": str(tmp_path / "test.db"),
        "LECTERN_HOST": "127.0.0.1", "LECTERN_PORT": str(port),
        "LECTERN_BASE_URL": url, "LECTERN_API": url,
        "LECTERN_AUTH_TOKEN": "", "LECTERN_GRIMOIRE_URL": "",
        "LECTERN_SESSION_POLL": "3600", "LECTERN_HANDOFF_POLL": "0.1",
        "LECTERN_TICK": "0.25", "LECTERN_CLAUDE_BIN": str(agent),
        "TMUX": "", "TMUX_TMPDIR": str(tmux_dir), "HOME": str(home),
        "XDG_STATE_HOME": str(tmp_path / "state"),
    }

    def api(path, data=None, method=None):
        req = urllib.request.Request(
            url + "/api" + path,
            data=json.dumps(data).encode() if data is not None else None,
            headers={"Content-Type": "application/json"}, method=method)
        return json.load(urllib.request.urlopen(req, timeout=20))

    log_path = tmp_path / "server.log"
    log = log_path.open("wb")
    proc = subprocess.Popen([_binary()], env=env, stdout=log, stderr=subprocess.STDOUT)
    try:
        for _ in range(100):
            if proc.poll() is not None:
                raise RuntimeError(f"server exited {proc.returncode}:\n{log_path.read_text(errors='replace')}")
            try:
                api("/health")
                break
            except Exception:
                time.sleep(0.1)
        else:
            raise RuntimeError("one-command server did not start:\n" + log_path.read_text(errors="replace"))
        api("/targets", {"name": "terminal-local", "kind": "local"})
        yield dict(url=url, env=env, api=api, tmp_path=tmp_path)
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=15)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
        log.close()
        for sock in tmux_dir.glob("tmux-*/default"):
            subprocess.run(["tmux", "-S", str(sock), "kill-server"], env=env,
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


def test_lectern_claude_creates_and_attaches_session_for_cwd(one_command_server, tmp_path):
    t = one_command_server
    project = tmp_path / "my-project"
    project.mkdir()

    master, slave = pty.openpty()
    child = subprocess.Popen([_binary(), "claude"], cwd=str(project),
                              stdin=slave, stdout=slave, stderr=slave,
                              env=t["env"], start_new_session=True)
    os.close(slave)
    output = b""

    def until(token, timeout=15):
        nonlocal output
        deadline = time.time() + timeout
        while time.time() < deadline:
            if token in output:
                return
            if select.select([master], [], [], 0.1)[0]:
                try:
                    output += os.read(master, 65536)
                except OSError:
                    break
        raise AssertionError(f"never saw {token!r} in:\n{output.decode(errors='replace')}")

    try:
        until(b"Session #")
        until(b"stub agent ready")

        sessions = t["api"]("/sessions")
        assert len(sessions) == 1, sessions
        sess = sessions[0]
        assert sess["agent"] == "claude"
        assert sess["workdir"] == str(project.resolve())
        assert sess["name"] == "my-project"

        # Ctrl-b d detaches; the CLI, which handed the process image over to
        # the attachment, exits with it.
        os.write(master, b"\x02d")
        deadline = time.monotonic() + 15
        while child.poll() is None and time.monotonic() < deadline:
            try:
                if select.select([master], [], [], 0.1)[0]:
                    output += os.read(master, 65536)
            except OSError:
                pass
        assert child.poll() == 0, f"lectern claude did not exit cleanly:\n{output.decode(errors='replace')}"

        # The session survives the detach — it's still tracked and live.
        after = t["api"]("/sessions")
        assert len(after) == 1 and after[0]["id"] == sess["id"]
    finally:
        if child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                child.kill()
        try:
            os.close(master)
        except OSError:
            pass


def test_lectern_claude_reuses_existing_session_non_interactively(one_command_server, tmp_path):
    """A non-interactive caller (no TTY) defaults to reusing a live session
    for the same agent and folder rather than starting a second one."""
    t = one_command_server
    project = tmp_path / "reuse-project"
    project.mkdir()

    first = t["api"]("/sessions", {
        "agent": "claude", "workdir": str(project.resolve()), "name": "reuse-project",
    })

    result = subprocess.run([_binary(), "claude"], cwd=str(project), env=t["env"],
                             capture_output=True, text=True, timeout=20)
    # No TTY on either end: startAttachment falls back to directAttachment,
    # which execs `tmux attach` against the real session and returns once
    # that (non-interactive) attach exits — it should not create a second
    # tracked session for the same agent+folder.
    assert f'Session #{first["id"]}' in result.stdout, result.stdout + result.stderr

    sessions = t["api"]("/sessions")
    assert len(sessions) == 1
    assert sessions[0]["id"] == first["id"]
