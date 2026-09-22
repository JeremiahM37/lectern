"""Real PTY proof for promoting the native conversation in a tracked shell.

The agent fixture is deliberately a small native identity writer: it owns a
real transcript FD and process receipt, but makes no paid Claude/Codex call.
"""
import json
import os
import fcntl
import pty
import select
import struct
import sqlite3
import subprocess
import termios
import time
import uuid
import urllib.request
import urllib.error
import pytest
from pathlib import Path

from conftest import _binary
from test_terminal_workspace import real_terminal


def _pty(binary, env, *args):
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 100, 0, 0))

    def tty():
        os.setsid()
        fcntl.ioctl(slave, termios.TIOCSCTTY, 0)

    child = subprocess.Popen([str(binary), *args], stdin=slave, stdout=slave,
                             stderr=slave, env={**env, "TERM": "xterm-256color"},
                             preexec_fn=tty)
    os.close(slave)
    return master, child


def _read(master, token, timeout=20, output=b""):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if token in output:
            return output
        if select.select([master], [], [], .2)[0]:
            try:
                output += os.read(master, 65536)
            except OSError:
                break
    raise AssertionError("missing %r in fresh PTY output: %r" % (token, output[-4000:]))


def _native_agent(t, agent="claude", marked=True):
    root = t["root"]
    # Use the product's quick-shell API, then cd into the tracked repository as
    # an operator would before starting Claude/Codex.
    shell = t["api"]("/shells", {"machine": "terminal-local"})
    t["id"] = shell["id"]
    row = shell
    home = root / "native-home"
    slug = "".join(c if c.isalnum() else "-" for c in str(root))
    folder = home / "projects" / slug
    folder.mkdir(parents=True)
    cid = "11111111-1111-4111-8111-111111111111"
    transcript = folder / (cid + ".jsonl")
    transcript.write_text("\n".join([
        json.dumps({"type": "user", "sessionId": cid, "cwd": str(root),
                    "message": {"role": "user", "content": "PROMOTION HISTORY PROOF"}}),
        json.dumps({"type": "assistant", "sessionId": cid, "cwd": str(root),
                    "message": {"role": "assistant", "content": "history retained"}}),
        "",
    ]))
    record_dir = home / "sessions"
    record_dir.mkdir()
    script = root / "native-agent.py"
    script.write_text("""import json, os, time
from pathlib import Path
home, cwd, cid = os.environ['NATIVE_HOME'], os.environ['NATIVE_CWD'], os.environ['NATIVE_CID']
pid = os.getpid()
start = Path('/proc', str(pid), 'stat').read_text().rsplit(')', 1)[1].split()[19]
Path(home, 'sessions', str(pid) + '.json').write_text(json.dumps({'pid': pid, 'procStart': start, 'sessionId': cid, 'cwd': cwd, 'kind': 'interactive', 'entrypoint': 'cli'}))
Path(cwd, 'native-ready').write_text(str(pid))
while True: time.sleep(.05)
""")
    row = t["api"]("/sessions/" + str(t["id"]))
    if not marked:
        # Exercise a preexisting legacy shell whose durable row and tmux pane
        # have no marker; promotion must validate first, then establish one.
        subprocess.run(["tmux", "set-option", "-u", "-t", "=" + row["tmux_session"] + ":",
                        "@lectern-tracking-identity"], env=t["env"], check=True)
        with sqlite3.connect(t["env"]["LECTERN_DB"]) as db:
            db.execute("UPDATE sessions SET tracking_identity='' WHERE id=?", (t["id"],))
            db.commit()
    # Start the native CLI as a child of the existing tracked blank shell.
    # Replacing the pane would change its lifecycle identity and is not the
    # operator flow this feature promises to preserve.
    command = ("cd %s && env CLAUDE_CONFIG_DIR=%s NATIVE_HOME=%s NATIVE_CWD=%s NATIVE_CID=%s "
               "python3 %s >/dev/null 2>&1 &" %
               (str(root), str(home), str(home), str(root), cid, str(script)))
    subprocess.run(["tmux", "send-keys", "-t", "=" + row["tmux_session"] + ":", command, "Enter"],
                   env=t["env"], check=True)
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline and not (root / "native-ready").exists():
        time.sleep(.05)
    assert (root / "native-ready").exists()
    t["native_home"] = home
    t["native_cid"] = cid
    t["native_pid"] = int((root / "native-ready").read_text())
    assert Path("/proc/%d/cwd" % t["native_pid"]).resolve() == root.resolve()
    assert (home / "sessions" / (str(t["native_pid"]) + ".json")).exists()
    # Native history lookup uses the configured agent profile, while the
    # tracked row remains the real shell's session record.
    req = urllib.request.Request(t["url"] + "/api/agents", method="PUT",
        data=json.dumps([{"name": "claude", "command": "claude",
                          "env": {"CLAUDE_CONFIG_DIR": str(home)}}]).encode(),
        headers={"Content-Type": "application/json"})
    urllib.request.urlopen(req, timeout=20).close()
    return row


def _promote(t, session_id, input_text):
    env = {**t["env"], "LECTERN_API": t["url"], "TMUX": ""}
    master, child = _pty(_binary(), env, "promote", str(session_id))
    try:
        output = _read(master, b"Detected session", output=b"")
        os.write(master, input_text.encode())
        output = _read(master, b"Conversation promoted", 20, output)
        child.wait(timeout=15)
        return output, child.returncode
    finally:
        if child.poll() is None:
            child.terminate(); child.wait(timeout=10)
        os.close(master)


def _json_request(t, method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(t["url"] + "/api" + path, method=method,
        data=data, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=20) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.loads(error.read())


@pytest.mark.parametrize("marked", [True, False], ids=["marked", "legacy-unmarked"])
def test_cli_promotes_live_native_conversation_in_place(real_terminal, marked):
    t = real_terminal
    before_projects = {p["id"] for p in t["api"]("/projects")}
    source = _native_agent(t, marked=marked)
    old_pid = t["native_pid"]
    output, code = _promote(t, source["id"], "n\rConversation proof\ryes\r")
    assert code == 0, output
    session = t["api"]("/sessions/" + str(source["id"]))
    projects = [p for p in t["api"]("/projects") if p["id"] not in before_projects]
    assert len(projects) == 1
    assert session["project_id"] == projects[0]["id"]
    assert projects[0]["repo_path"] == str(t["root"])
    assert projects[0]["default_agent"] == "claude"
    assert Path("/proc/%d" % old_pid).exists(), "promotion restarted native process"
    assert session["tmux_session"] == source["tmux_session"]
    assert session["id"] == source["id"]
    history = t["api"]("/sessions/%d/conversations/%s" % (source["id"], t["native_cid"]))
    assert any("PROMOTION HISTORY PROOF" in json.dumps(m) for m in history["messages"])
    # Promotion arms the native checkpoint observer for the now-agent session;
    # a later compaction/fork identity is captured without restarting the CLI.
    next_cid = "22222222-2222-4222-8222-222222222222"
    record = t["native_home"] / "sessions" / (str(old_pid) + ".json")
    native_record = json.loads(record.read_text())
    native_record["sessionId"] = next_cid
    record.write_text(json.dumps(native_record))
    deadline = time.monotonic() + 12
    while time.monotonic() < deadline:
        with sqlite3.connect(t["env"]["LECTERN_DB"]) as db:
            found = db.execute("SELECT native_recovery_cid FROM sessions WHERE id=?", (source["id"],)).fetchone()[0]
        if found == next_cid:
            break
        time.sleep(.1)
    assert found == next_cid
    # The tracked tmux terminal remains attachable after the CLI returns.
    subprocess.run(["tmux", "has-session", "-t", "=" + source["tmux_session"]],
                   env=t["env"], check=True)


def test_cli_refuses_missing_native_process_without_partial_project(real_terminal):
    t = real_terminal
    source = _native_agent(t)
    before_projects = {p["id"] for p in t["api"]("/projects")}
    subprocess.run(["tmux", "kill-session", "-t", "=" + source["tmux_session"]],
                   env=t["env"], check=True)
    env = {**t["env"], "LECTERN_API": t["url"], "TMUX": ""}
    master, child = _pty(_binary(), env, "promote", str(source["id"]))
    try:
        out = _read(master, b"conversation", 20).decode(errors="replace")
        child.wait(timeout=15)
        assert child.returncode != 0
        assert "native" in out.lower() or "process" in out.lower()
    finally:
        if child.poll() is None: child.terminate(); child.wait(timeout=10)
        os.close(master)
    assert {p["id"] for p in t["api"]("/projects")} == before_projects


def test_cli_cancellation_leaves_running_conversation_unmodified(real_terminal):
    t = real_terminal
    source = _native_agent(t)
    before_projects = {p["id"] for p in t["api"]("/projects")}
    env = {**t["env"], "LECTERN_API": t["url"], "TMUX": ""}
    master, child = _pty(_binary(), env, "promote", str(source["id"]))
    try:
        _read(master, b"Project", 20)
        os.write(master, b"\x03")
        child.wait(timeout=15)
        assert child.returncode in (0, 1, 130, -2)
    finally:
        if child.poll() is None: child.terminate(); child.wait(timeout=10)
        os.close(master)
    row = t["api"]("/sessions/" + str(source["id"]))
    assert row.get("project_id") is None
    assert {p["id"] for p in t["api"]("/projects")} == before_projects
    assert Path("/proc/%d" % t["native_pid"]).exists()


def test_api_rejects_native_cid_switch_without_partial_project(real_terminal):
    t = real_terminal
    source = _native_agent(t)
    status, preview = _json_request(t, "GET", "/sessions/%d/promote/preview" % source["id"])
    assert status == 200
    record = t["native_home"] / "sessions" / (str(t["native_pid"]) + ".json")
    changed = json.loads(record.read_text())
    changed["sessionId"] = "22222222-2222-4222-8222-222222222222"
    record.write_text(json.dumps(changed))
    before = t["api"]("/sessions/%d" % source["id"])
    status, _ = _json_request(t, "POST", "/sessions/%d/promote" % source["id"],
                              {"name": "must-not-exist", "expected_identity": preview["identity"]})
    assert status == 409
    after = t["api"]("/sessions/%d" % source["id"])
    assert after.get("project_id") is None
    assert after["id"] == before["id"] and after["tmux_session"] == before["tmux_session"]
    assert not any(p["name"] == "must-not-exist" for p in t["api"]("/projects"))


def test_api_rejects_existing_project_on_wrong_directory_without_mutation(real_terminal):
    t = real_terminal
    source = _native_agent(t)
    project = t["api"]("/projects", {"name": "wrong-directory", "target_id": t["target_id"],
                                      "repo_path": str(t["root"] / "elsewhere")})
    status, preview = _json_request(t, "GET", "/sessions/%d/promote/preview" % source["id"])
    assert status == 200
    status, _ = _json_request(t, "POST", "/sessions/%d/promote" % source["id"],
                              {"project_id": project["id"], "expected_identity": preview["identity"]})
    assert status in (409, 422)
    row = t["api"]("/sessions/%d" % source["id"])
    assert row.get("project_id") is None
    retained = next(p for p in t["api"]("/projects") if p["id"] == project["id"])
    assert retained["repo_path"] == str(t["root"] / "elsewhere")
