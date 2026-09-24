"""Real PTY coverage for the quick blank-shell CLI and TUI action.

These tests deliberately use the real target executors, tmux and a real SSH
transport.  The shell is tracked by Lectern, but it has no project or agent
wizard attached to it; its working directory is a target-owned scratch room.
"""

import json
import os
import fcntl
import pty
import re
import select
import struct
import subprocess
import termios
import time
from pathlib import Path

from conftest import _binary
from test_remote_acceptance import _ssh, remote_terminal
from test_local_mode import _local_env, _run as _local_run, local_binary
from test_terminal_workspace import real_terminal


def _cli(t, *args):
    return [_binary(), *args]


def _spawn_process(binary, env, *args):
    master, slave = pty.openpty()
    # Bubble Tea and tmux use the initial PTY geometry when rendering the
    # picker and shell.  openpty's default is 0×0, which produces a wrapped or
    # invisible first frame on some libc/terminal combinations.
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))

    def controlling_tty():
        os.setsid()
        fcntl.ioctl(slave, termios.TIOCSCTTY, 0)

    child = subprocess.Popen(
        [str(binary), *args],
        stdin=slave,
        stdout=slave,
        stderr=slave,
        env={**env, "TERM": "xterm-256color"},
        preexec_fn=controlling_tty,
    )
    os.close(slave)
    return master, child


def _pty_cli(t, *args):
    return _spawn_process(_binary(), {**t["env"], "LECTERN_API": t["url"]}, *args)


def _read_until(master, token, timeout=20, output=b""):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if token in output:
            return output
        if select.select([master], [], [], 0.2)[0]:
            try:
                output += os.read(master, 65536)
            except OSError:
                break
    raise AssertionError(
        "did not see %r in PTY output:\n%s" % (token, output.decode(errors="replace"))
    )


def _new_session(t, before):
    rows = t["api"]("/sessions")
    candidates = [row for row in rows if row["id"] not in before]
    assert len(candidates) == 1, rows
    return candidates[0]


def _make_picker_ambiguous(t):
    """Ensure the CLI must exercise its searchable machine picker."""
    t["api"]("/targets", {"name": "terminal-secondary", "kind": "local"})


def _wait_file(path: Path, expected: str, timeout=15):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if path.exists() and path.read_text() == expected:
            return
        time.sleep(0.1)
    assert path.exists(), "expected shell proof file %s" % path
    assert path.read_text() == expected


def _detach(master, child):
    os.write(master, b"\x02d")
    child.wait(timeout=15)
    assert child.returncode == 0


def _kill_remote_shell(t, row):
    if not row:
        return
    env = "env HOME=%s SHELL=/bin/bash TMUX=%s TMUX_TMPDIR=%s" % (
        str(t["remote_home"]), "", str(t["remote_tmux"])
    )
    _ssh(t, "%s tmux -f /dev/null kill-session -t =%s || true" % (env, row["tmux_session"]))


def test_cli_shell_picks_local_machine_and_keeps_blank_room(real_terminal):
    t = real_terminal
    _make_picker_ambiguous(t)
    before = {row["id"] for row in t["api"]("/sessions")}
    master, child = _pty_cli(t, "shell")
    output = b""
    row = None
    try:
        # The picker is a line-oriented searchable prompt.  Supplying the
        # complete target name proves the search path rather than relying on
        # an incidental first row.
        output = _read_until(master, b"terminal-local", 20, output)
        os.write(master, b"terminal-local\r")
        output = _read_until(master, b"$", 20, output)
        assert not output.lstrip().startswith(b"{")

        row = _new_session(t, before)
        assert row.get("project_id") is None
        assert row["target_id"] == t["target_id"]
        scratch_root = Path(t["env"]["HOME"]) / "lectern-scratch"
        assert row["workdir"].startswith(str(scratch_root))
        assert row["agent"] == "shell"

        marker = Path(row["workdir"]) / "quick-shell-local.txt"
        os.write(master, b"printf local-proof > quick-shell-local.txt\r")
        _wait_file(marker, "local-proof")
        _detach(master, child)

        # Detach leaves the exact tracked terminal alive and reattachment does
        # not create a second session or a second scratch directory.
        subprocess.run(
            ["tmux", "has-session", "-t", "=" + row["tmux_session"]],
            env=t["env"],
            check=True,
        )
        before_reattach = {r["id"] for r in t["api"]("/sessions")}
        master2, child2 = _pty_cli(t, "attach", "session", str(row["id"]))
        try:
            _read_until(master2, b"local-proof", 20)
            assert {r["id"] for r in t["api"]("/sessions")} == before_reattach
            os.write(master2, b"printf second-proof > quick-shell-second.txt\r")
            _wait_file(Path(row["workdir"]) / "quick-shell-second.txt", "second-proof")
            _detach(master2, child2)
        finally:
            if child2.poll() is None:
                child2.terminate()
                child2.wait(timeout=10)
            os.close(master2)
    finally:
        if child.poll() is None:
            child.terminate()
            child.wait(timeout=10)
        os.close(master)


def test_cli_shell_runs_on_the_selected_ssh_machine(remote_terminal):
    t = remote_terminal
    before = {row["id"] for row in t["api"]("/sessions")}
    master, child = _pty_cli(t, "shell", t["remote_target"]["name"])
    output = b""
    row = None
    try:
        output = _read_until(master, b"$", 30, output)
        assert not output.lstrip().startswith(b"{")
        row = _new_session(t, before)
        assert row["target_id"] == t["remote_target"]["id"]
        assert row.get("project_id") is None
        assert row["workdir"].startswith(str(t["remote_home"] / "lectern-scratch"))

        os.write(master, b"printf ssh-proof > quick-shell-ssh.txt\r")
        # The scratch suffix is generated by Lectern.  Resolve the exact
        # directory from the tracked row and verify the remote file there.
        remote_proof = Path(row["workdir"]) / "quick-shell-ssh.txt"
        _wait_file(remote_proof, "ssh-proof")
        _detach(master, child)
        _ssh(
            t,
            "env HOME=%s SHELL=/bin/bash TMUX=%s TMUX_TMPDIR=%s tmux has-session -t =%s"
            % (t["remote_home"], "", t["remote_tmux"], row["tmux_session"]),
        )
    finally:
        if child.poll() is None:
            child.terminate()
            child.wait(timeout=10)
        os.close(master)
        _kill_remote_shell(t, row)


def test_tui_quick_shell_action_is_real_and_cancellable(real_terminal):
    t = real_terminal
    _make_picker_ambiguous(t)
    before = {row["id"] for row in t["api"]("/sessions")}
    master, child = _pty_cli(t, "console")
    output = b""
    try:
        output = _read_until(master, b"Lectern", 20, output)
        os.write(master, b"?")
        output = _read_until(master, b"Blank persistent shell", 20, output)
        clean = re.sub(rb"\x1b\[[0-?]*[ -/]*[@-~]", b"", output).decode(errors="replace")
        assert "S             Blank persistent shell in a project or on a machine" in clean, clean
        # Help closes on one key; S is the documented quick action.
        os.write(master, b"xS")
        # Discard the help frame: it has the same title as the form.
        output = _read_until(master, b"Blank persistent shell", 20, b"")
        # The one-field searchable shell form submits on Enter after the
        # machine name is filtered/committed.  This exercises the documented
        # one-step launch path rather than the generic Ctrl-s form shortcut.
        os.write(master, b"terminal-local\r")
        output = _read_until(master, b"$", 20, output)
        row = _new_session(t, before)
        assert row.get("project_id") is None
        assert row["agent"] == "shell"
        os.write(master, b"printf tui-proof > quick-shell-tui.txt\r")
        _wait_file(Path(row["workdir"]) / "quick-shell-tui.txt", "tui-proof")
        # The initial dashboard frame also contains the product name, so only
        # accept a freshly-read frame as proof that detach returned to it.
        os.write(master, b"\x02d")
        output = _read_until(master, b"Lectern", 20, b"")
        assert child.poll() is None
        os.write(master, b"q")
        child.wait(timeout=15)
        assert child.returncode == 0
    finally:
        if child.poll() is None:
            child.terminate()
            child.wait(timeout=10)
        os.close(master)

    # A fresh picker can be cancelled with Ctrl-C before submission; no new
    # tracked row, project, or task may appear as a side effect.
    before_cancel = {row["id"] for row in t["api"]("/sessions")}
    projects_before = {row["id"] for row in t["api"]("/projects")}
    tasks_before = {row["id"] for row in t["api"]("/tasks")}
    master2, child2 = _pty_cli(t, "shell")
    try:
        _read_until(master2, b"terminal-local", 20)
        os.write(master2, b"\x03")
        child2.wait(timeout=15)
        # Bubble Tea currently returns a cancellation error through the CLI,
        # which is an exit status of 1; a future signal-preserving client may
        # report 130. The invariant is that no resource was created.
        assert child2.returncode in (0, 1, 130)
        assert {row["id"] for row in t["api"]("/sessions")} == before_cancel
        assert {row["id"] for row in t["api"]("/projects")} == projects_before
        assert {row["id"] for row in t["api"]("/tasks")} == tasks_before
    finally:
        if child2.poll() is None:
            child2.terminate()
            child2.wait(timeout=10)
        os.close(master2)


def test_installed_local_shell_uses_private_runtime(local_binary, tmp_path):
    """The installed ``lectern local shell`` path works without a server."""
    binary, root = local_binary
    fake_agent = tmp_path / "unused-fake-agent"
    fake_agent.write_text("#!/bin/sh\nexec /bin/bash --noprofile --norc\n")
    fake_agent.chmod(0o755)
    env = _local_env(root, fake_agent)
    before = json.loads(_local_run(binary, env, "api", "GET", "/sessions").stdout)
    before_ids = {row["id"] for row in before}
    master, child = _spawn_process(binary, env, "local", "shell")
    row = None
    try:
        _read_until(master, b"$", 30)
        rows = json.loads(_local_run(binary, env, "api", "GET", "/sessions").stdout)
        candidates = [entry for entry in rows if entry["id"] not in before_ids]
        assert len(candidates) == 1, rows
        row = candidates[0]
        assert row["agent"] == "shell" and row.get("project_id") is None
        assert row["workdir"].startswith(str(root / "home" / "lectern-scratch"))
        os.write(master, b"printf installed-local-proof > installed-local-proof.txt\r")
        _wait_file(Path(row["workdir"]) / "installed-local-proof.txt", "installed-local-proof")
        _detach(master, child)
    finally:
        if child.poll() is None:
            child.terminate()
            child.wait(timeout=10)
        os.close(master)
        _local_run(binary, env, "stop", check=False)
