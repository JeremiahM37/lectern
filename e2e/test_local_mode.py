"""Standalone local command: source/binary install and durable local sessions."""

import json
import os
import pty
import select
import shlex
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from conftest import ROOT, _binary


@pytest.fixture(autouse=True)
def clean_board():
    """Local tests own their state and must not start the hosted fixture."""
    yield


def _run(binary, env, *args, input=None, check=True, timeout=30):
    return subprocess.run(
        [str(binary), "local", *args],
        env=env,
        input=input,
        text=True,
        capture_output=True,
        check=check,
        timeout=timeout,
    )


@pytest.fixture(scope="module")
def local_binary(tmp_path_factory):
    supplied = os.environ.get("LECTERN_LOCAL_TEST_BINARY") or os.environ.get("LECTERN_BIN")
    candidate = Path(supplied) if supplied else Path(_binary())
    probe = subprocess.run([candidate, "local", "--help"], text=True, capture_output=True)
    assert probe.returncode == 0, f"selected binary has no local runtime: {probe.stderr}"

    root = tmp_path_factory.mktemp("local-install")
    source_prefix = root / "source-bin"
    binary_prefix = root / "binary-bin"
    env = {
        **os.environ,
        "HOME": str(root / "home"),
        "XDG_BIN_HOME": str(root / "home/.local/bin"),
        "XDG_STATE_HOME": str(root / "state"),
        "XDG_CONFIG_HOME": str(root / "config"),
        "XDG_CACHE_HOME": str(root / "cache"),
        "PATH": os.environ.get("PATH", ""),
    }
    (root / "home").mkdir()
    source_install = subprocess.run(
        ["bash", str(ROOT / "tools/install-local.sh"), "--source", str(ROOT), "--prefix", str(source_prefix)],
        cwd=root,
        env=env,
        text=True,
        capture_output=True,
        check=False,
        timeout=120,
    )
    assert source_install.returncode == 0, source_install.stderr
    source_binary = source_prefix / "lectern"
    binary_install = subprocess.run(
        ["bash", str(ROOT / "tools/install-local.sh"), "--binary", str(source_binary), "--prefix", str(binary_prefix)],
        cwd=root,
        env=env,
        text=True,
        capture_output=True,
        check=False,
        timeout=30,
    )
    assert binary_install.returncode == 0, binary_install.stderr
    installed = binary_prefix / "lectern"
    assert installed.is_file() and os.access(installed, os.X_OK)
    return installed, root


def _local_env(root, fake_agent):
    tmux_tmp = root / "tmux"
    tmux_tmp.mkdir(exist_ok=True)
    env = {
        **os.environ,
        "HOME": str(root / "home"),
        "XDG_STATE_HOME": str(root / "state"),
        "XDG_CONFIG_HOME": str(root / "config"),
        "XDG_CACHE_HOME": str(root / "cache"),
        "TMUX_TMPDIR": str(tmux_tmp),
        "LECTERN_CLAUDE_BIN": str(fake_agent),
        "LECTERN_MOCK": "0",
        "LECTERN_SESSION_POLL": "3600",
    }
    env.pop("LECTERN_API", None)
    env.pop("LECTERN_ATTACH_HOST", None)
    # Local-runtime PTY assertions own their terminal.  If pytest itself is
    # running inside Lectern's tmux session, inheriting TMUX makes the CLI
    # deliberately open a display-popup instead of attaching to the fixture's
    # private socket; a raw PTY is not a tmux client and display-popup exits 1.
    # The popup behavior remains covered by attachmentInWorkspace unit tests.
    env.pop("TMUX", None)
    return env


def _json_command(binary, env, *args, **kwargs):
    result = _run(binary, env, *args, **kwargs)
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise AssertionError(f"expected JSON from {' '.join(args)}: {result.stdout!r} {result.stderr!r}") from exc


def _read_until(master, token, timeout=20):
    output = b""
    deadline = time.time() + timeout
    while time.time() < deadline:
        if token in output:
            return output
        ready, _, _ = select.select([master], [], [], 0.2)
        if ready:
            try:
                output += os.read(master, 65536)
            except OSError:
                break
    raise AssertionError(f"did not read {token!r}; got {output[-2000:]!r}")


def test_source_and_binary_local_install_create_persist_and_reattach(local_binary, tmp_path, request):
    binary, root = local_binary
    fake_agent = tmp_path / "fake-claude"
    fake_agent.write_text("#!/bin/sh\nprintf 'LOCAL_AGENT_READY\\n'\nexec /bin/bash --noprofile --norc\n")
    fake_agent.chmod(0o755)
    fake_task = tmp_path / "fake-task"
    task_ready = tmp_path / "local-task-ready"
    task_release = tmp_path / "local-task-release"
    fake_task.write_text(
        "#!/bin/sh\n"
        "cat >/dev/null\n"
        "mkdir -p .lectern\n"
        f"printf 'LOCAL_TASK_READY\\n' > {shlex.quote(str(task_ready))}\n"
        f"while [ ! -f {shlex.quote(str(task_release))} ]; do sleep 0.05; done\n"
        "printf 'LOCAL_TASK_SENTINEL\\n' > .lectern/local-task-sentinel\n"
    )
    fake_task.chmod(0o755)
    env = _local_env(root, fake_agent)
    workdir = tmp_path / "project"
    workdir.mkdir()
    subprocess.run(["git", "init", "-q", "-b", "main", str(workdir)], check=True)
    (workdir / "README.md").write_text("local runtime fixture\n")
    subprocess.run(["git", "-C", str(workdir), "add", "README.md"], check=True)
    subprocess.run(
        ["git", "-C", str(workdir), "-c", "user.name=Lectern Test", "-c",
         "user.email=lectern-test@example.invalid", "commit", "-qm", "initial"],
        check=True,
    )

    # A local engine owns a private tmux socket. Keep an unrelated session with
    # the same conventional name alive on the inherited default socket so this
    # test catches accidental attachment to the user's/default tmux namespace.
    collision_env = {**env, "TMUX": ""}
    collision_socket = None
    collision_name = "lec-s1"
    session_id = 0
    task_id = 0
    private_tmux = None

    def cleanup():
        # Register cleanup before starting the local helper, so assertion
        # failures cannot leave test sessions or an engine alive.
        # Release the handshake before cancellation so a running task can
        # write its final marker and exit cleanly during teardown.
        task_release.touch()
        if task_id:
            view = _run(binary, env, "api", "GET", f"/tasks/{task_id}", check=False)
            if view.returncode == 0:
                try:
                    state = json.loads(view.stdout).get("status")
                    if state == "review":
                        _run(binary, env, "api", "PATCH", f"/tasks/{task_id}",
                             json.dumps({"status": "done"}), check=False)
                    elif state in {"queued", "running"}:
                        _run(binary, env, "api", "POST", f"/tasks/{task_id}/cancel", "{}", check=False)
                except json.JSONDecodeError:
                    pass
        if session_id:
            _run(binary, env, "api", "DELETE", f"/sessions/{session_id}", check=False)
        _run(binary, env, "stop", check=False)
        for socket in (private_tmux, collision_socket):
            if socket:
                try:
                    names = subprocess.check_output(
                        ["tmux", "-S", str(socket), "list-sessions", "-F", "#{session_name}"],
                        env=env, text=True, stderr=subprocess.DEVNULL,
                    ).splitlines()
                except subprocess.CalledProcessError:
                    names = []
                for name in names:
                    if name and "\n" not in name and "\r" not in name:
                        subprocess.run(["tmux", "-S", str(socket), "kill-session", "-t", "="+name],
                                       env=env, check=False, capture_output=True)

    request.addfinalizer(
        cleanup
    )
    subprocess.run(
        ["tmux", "new-session", "-d", "-s", collision_name, "-c", str(workdir),
         "bash", "--noprofile", "--norc"],
        env=collision_env,
        check=True,
    )
    subprocess.run(
        ["tmux", "send-keys", "-t", collision_name,
         "printf 'COLLISION_%s\\n' 'SENTINEL'", "Enter"],
        env=collision_env,
        check=True,
    )
    collision_candidates = list(Path(collision_env["TMUX_TMPDIR"]).glob("tmux-*/default"))
    assert collision_candidates, "fixture tmux did not publish its default socket"
    collision_socket = collision_candidates[0]

    before = _json_command(binary, env, "status")
    assert before["state"] == "stopped"
    targets = _json_command(binary, env, "api", "GET", "/targets")
    target = next(row for row in targets if row["kind"] == "local")
    _json_command(
        binary,
        env,
        "api",
        "PUT",
        "/agents",
        json.dumps([{
            "name": "local-fake",
            "command": str(fake_agent),
            "task": {"command": str(fake_task), "prompt_template": "stdin", "output_mode": "plain"},
        }]),
    )
    project = _json_command(
        binary,
        env,
        "api",
        "POST",
        "/projects",
        json.dumps({"name": "Local project", "target_id": target["id"], "repo_path": str(workdir)}),
    )
    session = _json_command(
        binary,
        env,
        "api",
        "POST",
        "/sessions",
        json.dumps({"name": "Persistent local session", "project_id": project["id"], "agent": "local-fake"}),
    )
    session_id = session["id"]
    endpoint_file = root / "state" / "lectern" / "local" / "endpoint.json"
    endpoint = json.loads(endpoint_file.read_text())
    private_candidates = list(Path(endpoint["tmux_dir"]).glob("tmux-*/default"))
    assert private_candidates, endpoint
    private_tmux = private_candidates[0]
    assert private_tmux != collision_socket
    assert session["tmux_session"] == collision_name
    assert subprocess.run(
        ["tmux", "display-message", "-p", "-t", collision_name, "#{session_name}"],
        env=collision_env, capture_output=True, text=True, check=True,
    ).stdout.strip() == collision_name
    pane_pid = subprocess.run(
        ["tmux", "-S", str(private_tmux), "list-panes", "-t", session["tmux_session"],
         "-F", "#{pane_pid}"], capture_output=True, text=True, check=True,
    ).stdout.strip()
    assert pane_pid.isdigit()

    master, slave = pty.openpty()
    attached = subprocess.Popen(
        [str(binary), "local", "attach", "session", str(session["id"])],
        stdin=slave,
        stdout=slave,
        stderr=slave,
        env={**env, "TERM": "xterm-256color"},
        start_new_session=True,
    )
    os.close(slave)
    try:
        _read_until(master, b"LOCAL_AGENT_READY")
        os.write(master, b"printf 'LOCAL_%s\\n' 'PTY_SENTINEL'\r")
        _read_until(master, b"LOCAL_PTY_SENTINEL")
        # Let the shell finish repainting before sending the tmux prefix and
        # detach key, as a real terminal user would.
        time.sleep(0.5)
        os.write(master, b"\x02d")
        attached.wait(timeout=15)
        assert attached.returncode == 0
    finally:
        if attached.poll() is None:
            attached.terminate()
            attached.wait(timeout=10)
        os.close(master)

    assert _json_command(binary, env, "api", "GET", f"/sessions/{session['id']}")["name"] == "Persistent local session"
    collision_capture = subprocess.run(
        ["tmux", "capture-pane", "-p", "-t", collision_name], env=collision_env,
        capture_output=True, text=True, check=True,
    ).stdout
    assert "COLLISION_SENTINEL" in collision_capture
    assert "LOCAL_PTY_SENTINEL" not in collision_capture

    task = _json_command(
        binary,
        env,
        "api",
        "POST",
        "/tasks",
        json.dumps({
            "project_id": project["id"],
            "title": "Local task execution",
            "prompt": "write the local task marker",
            "agent": "local-fake",
        }),
    )
    task_id = task["id"]
    _json_command(binary, env, "api", "POST", f"/tasks/{task['id']}/dispatch", "{}")
    deadline = time.time() + 30
    task_view = None
    private_task_seen = False
    while time.time() < deadline:
        task_view = _json_command(binary, env, "api", "GET", f"/tasks/{task['id']}")
        running_attempt = task_view.get("attempt") or {}
        if not private_task_seen and task_view["status"] == "running" and running_attempt.get("tmux_session"):
            if task_ready.is_file():
                # The task holds its private tmux pane open until this release
                # file is created. Verify the namespace during that handshake,
                # before allowing the task to finish between API polls.
                private_task_seen = subprocess.run(
                    ["tmux", "-S", str(private_tmux), "has-session", "-t", running_attempt["tmux_session"]],
                    capture_output=True, check=False,
                ).returncode == 0
                if private_task_seen:
                    task_release.touch()
        if task_view["status"] in {"done", "review", "failed"}:
            break
        time.sleep(0.2)
    assert task_view and task_view["status"] in {"done", "review"}, task_view
    attempt = task_view.get("attempt") or {}
    marker = Path(attempt["worktree_path"]) / ".lectern" / "local-task-sentinel"
    assert marker.is_relative_to(root / "state"), attempt
    assert private_task_seen, "local task did not run in the private tmux namespace"
    assert marker.read_text() == "LOCAL_TASK_SENTINEL\n"
    _json_command(binary, env, "api", "PATCH", f"/tasks/{task['id']}", json.dumps({"status": "done"}))
    stopped = _run(binary, env, "stop")
    assert stopped.returncode == 0, stopped.stderr
    assert _json_command(binary, env, "status")["state"] == "stopped"

    sessions = _json_command(binary, env, "api", "GET", "/sessions")
    projects = _json_command(binary, env, "api", "GET", "/projects")
    assert any(row["id"] == session["id"] for row in sessions)
    assert any(row["id"] == project["id"] for row in projects)
    master, slave = pty.openpty()
    reattached = subprocess.Popen(
        [str(binary), "local", "attach", "session", str(session["id"])],
        stdin=slave, stdout=slave, stderr=slave,
        env={**env, "TERM": "xterm-256color"}, start_new_session=True,
    )
    os.close(slave)
    try:
        _read_until(master, b"LOCAL_AGENT_READY")
        os.write(master, b"printf 'LOCAL_%s\\n' 'REATTACH_SENTINEL'\r")
        _read_until(master, b"LOCAL_REATTACH_SENTINEL")
        time.sleep(0.5)
        os.write(master, b"\x02d")
        reattached.wait(timeout=15)
        assert reattached.returncode == 0
    finally:
        if reattached.poll() is None:
            reattached.terminate()
            reattached.wait(timeout=10)
        os.close(master)
    assert subprocess.run(
        ["tmux", "-S", str(private_tmux), "list-panes", "-t", session["tmux_session"],
         "-F", "#{pane_pid}"], capture_output=True, text=True, check=True,
    ).stdout.strip() == pane_pid
    collision_capture = subprocess.run(
        ["tmux", "capture-pane", "-p", "-t", collision_name], env=collision_env,
        capture_output=True, text=True, check=True,
    ).stdout
    assert "COLLISION_SENTINEL" in collision_capture
    assert "LOCAL_REATTACH_SENTINEL" not in collision_capture
    _run(binary, env, "stop", check=False)


def test_explicit_api_stays_remote(local_binary, tmp_path):
    binary, root = local_binary
    seen = []

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            seen.append(self.path)
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b"[]")

        def log_message(self, *_args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    env = {**os.environ, "LECTERN_API": f"http://127.0.0.1:{server.server_port}", "HOME": str(root / "remote-home")}
    try:
        result = subprocess.run([str(binary), "api", "GET", "/sessions"], env=env, text=True, capture_output=True, check=True)
        assert json.loads(result.stdout) == []
        assert seen == ["/api/sessions"]
    finally:
        server.shutdown()
