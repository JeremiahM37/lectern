"""Exercise a fresh demo installation and capture the actual UI for the README.

Run: .venv/bin/python tools/screenshots.py --grimoire-bin /usr/local/bin/grimoire
No production data, external agent calls, notifications, or DOM replacement.
The optional Grimoire process gets its own temporary vault and hashing embedder.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.request

from playwright.sync_api import expect, sync_playwright

ROOT = Path(__file__).resolve().parents[1]


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def api(base, path, body=None, method=None):
    req = urllib.request.Request(base + "/api" + path,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Content-Type": "application/json"}, method=method)
    with urllib.request.urlopen(req, timeout=20) as response:
        raw = response.read()
        return json.loads(raw) if raw else None


def wait_for(fn, timeout=25):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            value = fn()
            if value:
                return value
        except (OSError, ValueError):
            pass
        time.sleep(0.1)
    raise AssertionError("Demo did not reach its expected state")


def stop(proc):
    proc.terminate()
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--grimoire-bin", help="Optional Grimoire executable for the memory workflow")
    parser.add_argument("--output", type=Path, default=ROOT / "docs/screenshots")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    captures, checks = [], []
    with tempfile.TemporaryDirectory(prefix="lectern-demo-") as tmp_name:
        tmp = Path(tmp_name)
        binary = tmp / "lectern"
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/lectern"], cwd=ROOT, check=True)
        # Explicitly discard ambient app settings, especially outbound sinks,
        # provider URLs, credentials, and production database/vault paths.
        env = {k: v for k, v in os.environ.items() if not k.startswith(("LECTERN_", "GRIMOIRE_"))}
        processes, logs = [], []
        try:
            memory_base = ""
            if args.grimoire_bin:
                memory_base = f"http://127.0.0.1:{free_port()}"
                vault = tmp / "vault"
                vault.mkdir()
                (vault / "context-service.md").write_text("""---
title: Context service
---
# Context service

Decisions: keep human corrections when retrieved facts disagree. Store source
paths and writer authority with every fact. The deployment uses port 6432.

Next: verify the HTTP contract and document the handoff protocol.
""")
                mem_env = {**env, "GRIMOIRE_VAULT": str(vault), "GRIMOIRE_PORT": memory_base.rsplit(":", 1)[1],
                           "GRIMOIRE_HOST": "127.0.0.1", "GRIMOIRE_LOCAL_EMBED": "off", "GRIMOIRE_NO_WATCHER": "1"}
                log = open(tmp / "grimoire.log", "w")
                logs.append(log)
                processes.append(subprocess.Popen([str(Path(args.grimoire_bin).resolve())], env=mem_env,
                                                  cwd=tmp, stdout=log, stderr=log))
                wait_for(lambda: api(memory_base, "/health"))
                api(memory_base, "/memory", {"topic": "context-service", "text": "The context-service deployment port is 6432.",
                    "agent": "demo", "human": True})
            port = free_port()
            base = f"http://127.0.0.1:{port}"
            lec_env = {**env, "LECTERN_MOCK": "1", "LECTERN_PORT": str(port), "LECTERN_HOST": "127.0.0.1",
                "LECTERN_DB": str(tmp / "demo.db"), "LECTERN_TICK": "0.1", "LECTERN_MOCK_DELAY": "0.2",
                "LECTERN_SESSION_POLL": "0.2", "LECTERN_BASE_URL": base, "LECTERN_GRIMOIRE_URL": memory_base,
                "LECTERN_HOST_CLAUDE_CONFIG": str(tmp / "no-host-config.json"),
                "LECTERN_CREDS": str(tmp / "no-credentials"), "LECTERN_CODEX_CREDS": str(tmp / "no-codex-credentials")}
            log = open(tmp / "lectern.log", "w")
            logs.append(log)
            proc = subprocess.Popen([str(binary)], env=lec_env, cwd=ROOT, stdout=log, stderr=log)
            processes.append(proc)
            wait_for(lambda: api(base, "/health"))
            request = lambda path, body=None, method=None: api(base, path, body, method)
            # Demo mode seeds two examples. Remove only these temporary rows so
            # every project shown below is explicitly configured from scratch.
            for project in request("/projects"):
                request(f"/projects/{project['id']}", method="DELETE")
            for target in request("/targets"):
                request(f"/targets/{target['id']}", method="DELETE")
            assert request("/projects") == [] and request("/targets") == []
            target = request("/targets", {"name": "build-server", "kind": "mock", "max_concurrent": 4})
            laptop = request("/targets", {"name": "dev-workstation", "kind": "mock", "max_concurrent": 2})
            projects = [request("/projects", {"name": name, "target_id": target["id"],
                "repo_path": "/workspace/" + name, "review_gate": False})
                for name in ["web-console", "context-service", "release-tools"]]
            checks.append("Created two targets and three projects from an empty database")

            def task(title, project=0, dispatch=True, extra="", perm="acceptEdits"):
                row = request("/tasks", {"title": title, "project_id": projects[project]["id"],
                    "prompt": "Implement the change and verify the result. " + extra, "permission_mode": perm})
                if dispatch:
                    request(f"/tasks/{row['id']}/dispatch", {}, "POST")
                return row

            def wait_task(row, status):
                return wait_for(lambda: request(f"/tasks/{row['id']}")["status"] == status)

            with sync_playwright() as pw:
                browser = pw.chromium.launch()
                context = browser.new_context(viewport={"width": 1440, "height": 900}, device_scale_factor=1)
                page = context.new_page()
                errors = []
                page.on("pageerror", lambda err: errors.append(str(err)))

                def capture(name, locator=None):
                    page.evaluate("document.fonts.ready")
                    # Wait for a smooth scroll caused by a form opening to settle.
                    page.wait_for_timeout(250)
                    expect(page.locator("#toasts .toast")).to_have_count(0, timeout=10000)
                    (locator or page).screenshot(path=str(args.output / name), animations="disabled")
                    captures.append(name)

                def close_sheet():
                    page.locator("#sheet .x").click()

                def tab(name):
                    page.click(f".tab[data-tab='{name}']")

                def routines():
                    tab("board")
                    page.click("#qb-routines")

                page.goto(base)
                expect(page.locator("#conn-label")).to_have_text("LIVE")
                # Probe via the visible controls, then confirm the stored result.
                tab("targets")
                for t in [target, laptop]:
                    card = page.locator(".rowcard").filter(has=page.get_by_role("heading", name=t["name"], exact=True))
                    card.get_by_role("button", name="Probe", exact=True).click()
                    expect(card).to_contain_text("claude")
                checks.append("Both target probes succeeded through the UI")
                # Create, edit, pause/resume, and actually run a multi-project routine.
                routines()
                page.click("#rt-legend")
                page.fill("#rt-name", "Dependency review")
                page.select_option("#rt-projects", [str(p["id"]) for p in projects])
                page.fill("#rt-prompt", "Review dependency updates, run the project tests, and summarize any changes that need attention.")
                page.fill("#rt-schedule", "weekly on mon at 09:00")
                page.select_option("#rt-agent", "codex")
                page.click("#rt-save")
                card = page.locator("#rt-list .rowcard", has_text="Dependency review")
                expect(card).to_be_visible()
                card.get_by_role("button", name="Edit", exact=True).click()
                expect(page.locator("#rt-prompt")).to_have_value("Review dependency updates, run the project tests, and summarize any changes that need attention.")
                page.fill("#rt-name", "Weekly dependency review")
                page.click("#rt-save")
                card = page.locator("#rt-list .rowcard", has_text="Weekly dependency review")
                card.get_by_role("button", name="Pause", exact=True).click()
                expect(card).to_contain_text("(off)")
                card.get_by_role("button", name="Resume", exact=True).click()
                expect(card.get_by_role("button", name="Pause", exact=True)).to_be_visible()
                card.get_by_role("button", name="Run now").click()
                wait_for(lambda: len(request("/tasks")) == 3)
                wait_for(lambda: all(t["status"] == "review" for t in request("/tasks")))
                assert len(request("/routines")) == 1
                checks.append("Routine create/edit/pause/resume/run: exactly one task per selected project, no duplicate routine")
                close_sheet()
                # Clean up the completed smoke tasks before arranging the gallery.
                for row in request("/tasks"):
                    request(f"/tasks/{row['id']}/complete", {}, "POST")
                tab("board")
                page.reload()
                page.once("dialog", lambda d: d.accept())
                page.locator(".col.s-done .col-clear").click()
                wait_for(lambda: request("/tasks") == [])
                checks.append("Cleared a finished column without removing projects or routines")
                review = task("Add an API health endpoint")
                wait_task(review, "review")
                other = task("Preserve source and writer authority", 1)
                wait_task(other, "review")
                done = task("Improve keyboard navigation")
                wait_task(done, "review")
                request(f"/tasks/{done['id']}/complete", {}, "POST")
                done2 = task("Document deployment rollback", 2)
                wait_task(done2, "review")
                request(f"/tasks/{done2['id']}/complete", {}, "POST")
                failed = task("Investigate an integration failure", 2, extra="[mock:fail]")
                wait_task(failed, "failed")
                task("Add saved search filters", dispatch=False)
                task("Expand retrieval regression cases", 1, dispatch=False)
                task("Prepare the next release notes", 2, dispatch=False)
                request("/routines", {"name": "Release readiness", "prompt": "Run tests, check the changelog, and report anything still blocking the next release.",
                    "project_ids": [projects[2]["id"]], "agent": "claude", "schedule": ""})
                request("/routines", {"name": "Daily project health", "prompt": "Check project tests and summarize failures with reproducible steps.",
                    "project_ids": [p["id"] for p in projects], "schedule": "daily at 09:00", "agent": "claude"})
                request(f"/targets/{target['id']}", {"max_concurrent": 1}, "PATCH")
                gated = task("Review the deployment cleanup", 2, extra="[mock:approval]", perm="default")
                wait_for(lambda: request("/approvals"))
                queued = task("Run the release checks", 2)
                wait_task(queued, "queued")
                sessions = []
                for name, agent, index in [("Mobile navigation", "claude", 0), ("Memory continuity", "codex", 1), ("Retrieval regression tests", "claude", 1)]:
                    sessions.append(request("/sessions", {"project_id": projects[index]["id"], "name": name, "agent": agent}))
                wait_for(lambda: all(s["status"] != "starting" for s in request("/sessions")))
                page.reload()
                expect(page.locator(".col.s-queued .card", has_text=queued["title"])).to_be_visible()
                capture("board.png")
                page.locator(".col.s-review .card", has_text=review["title"]).click()
                expect(page.locator(".ev.e-result").first).to_be_visible()
                capture("timeline.png", page.locator("#sheet"))
                page.click("#actions button:has-text('Diff')")
                expect(page.locator(".dfile").first).to_be_visible()
                capture("diff.png", page.locator("#sheet"))
                close_sheet()
                tab("sessions")
                expect(page.locator(".scard")).to_have_count(3)
                capture("sessions.png")
                card = page.locator(".scard", has_text="Memory continuity")
                card.get_by_role("button", name="Handoff").click()
                page.select_option("#ho-agent", "claude")
                page.uncheck("#ho-kill")
                capture("handoff.png", page.locator("#sheet"))
                page.click("#ho-go")
                wait_for(lambda: len(request("/sessions")) == 4)
                wraps = request(f"/sessions/{sessions[1]['id']}/wraps")
                assert wraps and "lectern:complete" not in wraps[0]["summary"]
                assert request(f"/sessions/{sessions[1]['id']}")["status"] != "dead"
                checks.append("Cross-agent handoff saved a complete wrap and preserved the predecessor when requested")
                # Prime a new session against the optional real, isolated Grimoire.
                page.click("#sess-new")
                page.select_option("#ns-project", str(projects[1]["id"]))
                page.fill("#ns-name", "Continue the memory review")
                page.select_option("#ns-agent", "codex")
                page.select_option("#ns-start", "brief")
                if memory_base:
                    expect(page.locator("#ns-memory-status")).to_have_text("Project memory loaded.")
                    brief = request(f"/projects/{projects[1]['id']}/brief")
                    assert any(f["authority"] == "human" for f in brief["memory"]["facts"])
                    assert '"trust":' in brief["brief"]
                capture("session-start.png", page.locator("#sheet"))
                page.click("#ns-go")
                wait_for(lambda: any(s["name"] == "Continue the memory review" for s in request("/sessions")))
                checks.append("Fresh session launched with an explicit memory status" +
                    (" and provenance-bearing Grimoire brief" if memory_base else " (no memory provider configured)"))
                tab("deck")
                expect(page.locator(".pane-line").first).to_be_visible()
                capture("deck.png")
                tab("approvals")
                expect(page.locator(".rowcard").first).to_contain_text("wants to run")
                capture("approvals.png")
                # Approve from the phone and verify the queued task also progresses.
                page.set_viewport_size({"width": 390, "height": 844})
                capture("mobile-approval.png")
                page.get_by_role("button", name="Approve", exact=True).click()
                wait_task(gated, "review")
                wait_task(queued, "review")
                checks.append("Phone approval unblocked the agent and released the target slot for queued work")
                page.set_viewport_size({"width": 1440, "height": 900})
                routines()
                expect(page.locator("#rt-list .rowcard")).to_have_count(3)
                capture("routines.png", page.locator("#sheet"))
                card = page.locator("#rt-list .rowcard", has_text="Weekly dependency review")
                card.get_by_role("button", name="Edit", exact=True).click()
                expect(page.locator("#rt-name")).to_have_value("Weekly dependency review")
                page.locator("#rt-form").scroll_into_view_if_needed()
                capture("routine-edit.png", page.locator("#rt-form"))
                close_sheet()
                tab("targets")
                expect(page.locator("#running-build")).to_contain_text(request("/health")["build"]["version"])
                capture("targets.png")
                page.fill("#pj-search", "context-service")
                page.locator(".pjrow", has_text="context-service").click()
                expect(page.locator(".cap-sel")).to_be_visible()
                page.select_option(".cap-sel", "parity")
                wait_for(lambda: next(p for p in request("/projects") if p["id"] == projects[1]["id"])["capability_profile"] == "parity")
                capture("project-settings.png", page.locator("#sheet .rowcard"))
                close_sheet()
                tab("targets")
                spend = page.locator(".rowcard").filter(has=page.get_by_role("heading", name="Spend", exact=True))
                expect(spend).to_contain_text("all-time")
                capture("spend.png", spend)
                checks.append("Project capability changes persisted; spend totals populated from actual scripted attempts")
                page.set_viewport_size({"width": 390, "height": 844})
                tab("board")
                expect(page.locator(".colstrip")).to_be_visible()
                assert page.evaluate("document.body.scrollWidth <= innerWidth")
                capture("mobile-board.png")
                tab("sessions")
                expect(page.locator(".scard").first).to_be_visible()
                capture("mobile.png")
                routines()
                expect(page.locator("#rt-list")).to_contain_text("Daily project health")
                capture("mobile-routines.png")
                assert not errors, errors
                browser.close()
            # Reopen the real database with a new server process; mock agents
            # themselves are in-memory, so persistence is checked on durable rows.
            snapshot = {kind: [r["id"] for r in request('/' + kind)] for kind in ["projects", "routines", "tasks"]}
            stop(proc)
            processes.remove(proc)
            proc = subprocess.Popen([str(binary)], env=lec_env, cwd=ROOT, stdout=log, stderr=log)
            processes.append(proc)
            wait_for(lambda: api(base, "/health"))
            for kind, ids in snapshot.items():
                assert [r["id"] for r in request('/' + kind)] == ids
            checks.append("Projects, routines, and completed task history survived a service restart")
            report = {"screenshots": captures, "checks": checks, "grimoire": bool(memory_base), "sample_data": True}
            (args.output / "capture-report.json").write_text(json.dumps(report, indent=2) + "\n")
            print(f"PASS: {len(checks)} demo workflow checks; captured {len(captures)} screenshots")
        except Exception:
            # Preserve diagnostics outside the temp directory without ever copying
            # production logs or credentials into documentation assets.
            diagnostics = Path(tempfile.mkdtemp(prefix="lectern-demo-failure-"))
            for file in tmp.glob("*.log"):
                shutil.copyfile(file, diagnostics / file.name)
            print(f"Demo logs: {diagnostics}")
            raise
        finally:
            for process in reversed(processes):
                stop(process)
            for file in logs:
                file.close()


if __name__ == "__main__":
    main()
