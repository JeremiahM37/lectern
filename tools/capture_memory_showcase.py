"""Capture current UIs using disposable demo data and verify real memory wiring."""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.request

from playwright.sync_api import expect, sync_playwright

ROOT = Path(__file__).resolve().parents[1]


def port():
    with socket.socket() as connection:
        connection.bind(("127.0.0.1", 0))
        return connection.getsockname()[1]


def api(base, path, body=None, method=None):
    request = urllib.request.Request(base + "/api" + path,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json"}, method=method)
    with urllib.request.urlopen(request, timeout=20) as response:
        return json.load(response)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--grimoire-root", type=Path, required=True)
    parser.add_argument("--lectern-bin", type=Path, required=True)
    args = parser.parse_args()
    grimoire = args.grimoire_root.resolve()
    binary = args.lectern_bin.resolve()
    deck_output = ROOT / "docs/screenshots"
    memory_output = grimoire / "docs/screenshots"
    with tempfile.TemporaryDirectory(prefix="memory-showcase-") as directory:
        temporary = Path(directory)
        home = temporary / "home"
        home.mkdir()
        tmux_root = temporary / "tmux"
        tmux_root.mkdir()
        environment = {key: value for key, value in os.environ.items()
            if not key.startswith(("LECTERN_", "GRIMOIRE_", "ANTHROPIC_", "OPENAI_"))}
        environment.update(HOME=str(home), TMUX="", TMUX_TMPDIR=str(tmux_root),
            XDG_CONFIG_HOME=str(home / ".config"), XDG_STATE_HOME=str(home / ".state"))
        processes = []
        logs = []

        def start(command, settings, label):
            log = (temporary / (label + ".log")).open("w")
            logs.append(log)
            process = subprocess.Popen(command, cwd=temporary,
                env={**environment, **settings}, stdout=log, stderr=log)
            processes.append(process)
            return process

        def ready(base):
            for _ in range(150):
                try:
                    return api(base, "/health")
                except OSError:
                    time.sleep(.1)
            raise RuntimeError("demo server did not start")

        try:
            memory_port, deck_port, terminal_port, console_port = port(), port(), port(), port()
            memory_base, deck_base = f"http://127.0.0.1:{memory_port}", f"http://127.0.0.1:{deck_port}"
            terminal_base = f"http://127.0.0.1:{terminal_port}"
            start([str(grimoire / "go/grimoire")], {
                "GRIMOIRE_VAULT": str(temporary / "vault"), "GRIMOIRE_PORT": str(memory_port),
                "GRIMOIRE_HOST": "127.0.0.1", "GRIMOIRE_LOCAL_EMBED": "off",
                "GRIMOIRE_NO_WATCHER": "1", "GRIMOIRE_WEB_DIR": str(grimoire / "frontend/dist"),
            }, "grimoire")
            ready(memory_base)
            spec = importlib.util.spec_from_file_location("demo_notes", grimoire / "tools/screenshots.py")
            demo = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(demo)
            for path, frontmatter, body in demo.NOTES:
                api(memory_base, "/notes", {"path": path, "frontmatter": frontmatter, "body": body})
            start([str(binary), "serve"], {
                "LECTERN_MOCK": "1", "LECTERN_PORT": str(deck_port), "LECTERN_HOST": "127.0.0.1",
                "LECTERN_DB": str(temporary / "deck.db"), "LECTERN_GRIMOIRE_URL": memory_base,
                "LECTERN_BASE_URL": deck_base, "LECTERN_TICK": "0.1",
                "LECTERN_MOCK_DELAY": "0.15", "LECTERN_SESSION_POLL": "0.2",
            }, "deck")
            ready(deck_base)
            target = api(deck_base, "/targets")[0]
            projects = [api(deck_base, "/projects", {"name": name, "target_id": target["id"],
                "repo_path": "/demo/" + name}) for name in ["kestrel", "context-service", "release-tools"]]
            project = projects[1]
            assert project["memory_status"] == "ready"
            topic = project["memory_topic"]
            for fact in ["The context-service deployment port is 6432.",
                         "Human corrections take precedence over agent-generated updates.",
                         "Every release includes a documented rollback procedure."]:
                api(memory_base, "/memory", {"topic": topic, "scope": "topic", "text": fact, "human": True})
            api(memory_base, "/memory", {"topic": projects[0]["memory_topic"],
                "text": "OTHER_PROJECT_SENTINEL", "infer": False})
            brief = api(deck_base, f"/projects/{project['id']}/brief")
            assert "6432" in brief["brief"] and "OTHER_PROJECT_SENTINEL" not in brief["brief"]
            renamed = api(deck_base, f"/projects/{project['id']}", {"name": "context-platform"}, "PATCH")
            assert renamed["memory_topic"] == topic
            assert "6432" in api(deck_base, f"/projects/{project['id']}/brief")["brief"]
            for title, index in [("Add scoped memory regression cases", 1), ("Improve keyboard navigation", 0),
                                 ("Document release rollback", 2), ("Review dependency updates", 0)]:
                api(deck_base, "/tasks", {"project_id": projects[index]["id"], "title": title,
                    "prompt": title, "agent": "claude"})
            for name, agent, index in [("Memory integration", "codex", 1), ("Mobile navigation", "claude", 0)]:
                api(deck_base, "/sessions", {"project_id": projects[index]["id"], "name": name, "agent": agent})

            workspace = temporary / "kestrel"
            workspace.mkdir()
            (workspace / "README.md").write_text("# Kestrel\n\nA sample project for the terminal walkthrough.\n")
            subprocess.run(["git", "init", "-q", str(workspace)], check=True, env=environment)
            subprocess.run(["tmux", "-f", "/dev/null", "new-session", "-d", "-s", "showcase", "-c", str(workspace),
                "bash", "--noprofile", "--norc"], check=True, env=environment)
            subprocess.run(["tmux", "send-keys", "-t", "=showcase:", "printf '\\nKestrel development workspace\\nProject files remain on your own machine.\\n\\n'; ls", "Enter"], check=True, env=environment)
            start([str(binary), "serve"], {"LECTERN_MOCK": "0", "LECTERN_HOST": "127.0.0.1",
                "LECTERN_PORT": str(terminal_port), "LECTERN_DB": str(temporary / "terminal.db"),
                "LECTERN_BASE_URL": terminal_base, "LECTERN_SESSION_POLL": "3600"}, "terminal")
            ready(terminal_base)
            local = api(terminal_base, "/targets", {"name": "local-demo", "kind": "local"})
            session = api(terminal_base, "/sessions/adopt", {"target_id": local["id"],
                "tmux_session": "showcase", "workdir": str(workspace), "name": "Kestrel workspace", "agent": "claude"})
            start(["ttyd", "-i", "127.0.0.1", "-p", str(console_port), "-t", "fontSize=18", "-W", str(binary)],
                {"LECTERN_API": deck_base, "TERM": "xterm-256color"}, "console")
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch()
                page = browser.new_page(viewport={"width": 1440, "height": 960})
                page.add_init_script("localStorage.setItem('grimoire-theme', 'dark')")
                errors = []
                page.on("pageerror", lambda error: errors.append(str(error)))

                def shot(path):
                    page.evaluate("document.fonts.ready")
                    page.wait_for_timeout(600)
                    page.screenshot(path=str(path), animations="disabled")

                page.goto(deck_base)
                expect(page.locator("#conn-label")).to_have_text("LIVE")
                shot(deck_output / "board-current.png")
                page.goto(deck_base + "/#sessions")
                expect(page.locator(".scard").first).to_be_visible()
                shot(deck_output / "sessions-current.png")
                page.click("#sess-new")
                page.select_option("#ns-project", str(project["id"]))
                page.select_option("#ns-start", "brief")
                expect(page.locator("#ns-memory-status")).to_contain_text("memory loaded")
                shot(deck_output / "project-memory-current.png")
                page.goto(terminal_base + f"/terminal/session/{session['id']}")
                expect(page.locator("#connection")).to_have_text("Connected", timeout=20000)
                expect(page.locator("#agent-terminal .xterm-screen")).to_contain_text("Kestrel development workspace")
                page.locator("#terminal-tools > summary").click()
                page.locator("#shell").click()
                expect(page.locator("#shell-terminal")).to_be_visible()
                shot(deck_output / "terminal-workspace-current.png")
                frames = []
                page.set_viewport_size({"width": 1280, "height": 720})
                page.on("websocket", lambda connection: connection.on("framereceived", lambda frame: frames.append(frame.decode(errors="replace") if isinstance(frame, bytes) else frame)))
                page.goto(f"http://127.0.0.1:{console_port}")
                for _ in range(100):
                    if "Lectern" in "".join(frames) and "LIVE" in "".join(frames):
                        break
                    page.wait_for_timeout(100)
                assert "Lectern" in "".join(frames) and "LIVE" in "".join(frames)
                shot(deck_output / "terminal-console-current.png")
                page.set_viewport_size({"width": 1440, "height": 960})
                page.goto(memory_base + "/#engineering/deployment-runbook.md")
                expect(page.locator("#title")).to_have_value("Deployment Runbook")
                shot(memory_output / "editor-current.png")
                page.goto(memory_base + "/#memory/" + topic + ".md")
                expect(page.locator("#title")).to_have_value("Memory: context-service")
                shot(memory_output / "project-memory-current.png")
                page.keyboard.press("Control+k")
                page.fill("#palette-input", "graph")
                page.locator("#palette-list .pal-item").filter(has_text="Graph").first.click()
                page.wait_for_timeout(700)
                shot(memory_output / "graph-current.png")
                assert not errors, errors
                browser.close()
            report = {"sample_data": True, "real_terminal": True,
                "checks": ["Project creation provisioned a note in real Grimoire", "Recall excluded another project",
                           "Rename retained memory", "Real tmux/ttyd workspace connected", "Native terminal dashboard rendered"],
                "lectern_build": api(deck_base, "/health")["build"],
                "grimoire_build": api(memory_base, "/health")["build"]}
            (deck_output / "memory-showcase-report.json").write_text(json.dumps(report, indent=2) + "\n")
            print("PASS: real project-memory integration and eight current UI captures")
        finally:
            for process in reversed(processes):
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
            for connection in tmux_root.glob("tmux-*/default"):
                subprocess.run(["tmux", "-S", str(connection), "kill-server"], env=environment,
                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
            for log in logs:
                log.close()


if __name__ == "__main__":
    main()
