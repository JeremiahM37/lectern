"""The first-run checklist: what a brand-new install shows instead of an
empty kanban board with nothing to click, and that its one action always
leads somewhere real (docs/../BUILD item 3 — "no dead ends").

Unlike every other e2e test here, this one deliberately does NOT use the
shared `server` fixture: that server runs in mock mode, which seeds a demo
project/target/session on startup (see App.SeedDemoData) specifically so the
rest of the suite always has something to click. The first-run screen only
exists to be shown before any of that is true, so this file starts its own
non-mock server against a brand-new, empty database — the exact state
`lectern up` finds on a machine that has never run Lectern before.
"""
import os
import socket
import subprocess
import tempfile
import time
from pathlib import Path

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE, _binary, _port_open


def _unused_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


@pytest.fixture(scope="module")
def blank_server():
    """A real, non-mock Lectern with nothing in its database: no seeded
    project, target, or session."""
    port = _unused_port()
    tmp = tempfile.mkdtemp(prefix="lec-e2e-firstrun-")
    env = {
        **os.environ,
        "LECTERN_MOCK": "0",
        "LECTERN_PORT": str(port),
        "LECTERN_HOST": "127.0.0.1",
        "LECTERN_DB": str(Path(tmp) / "blank.db"),
        "LECTERN_BASE_URL": f"http://127.0.0.1:{port}",
        "LECTERN_AUTH": "none",
        "HOME": str(Path(tmp) / "home"),
    }
    Path(env["HOME"]).mkdir(parents=True, exist_ok=True)
    proc = subprocess.Popen(
        [_binary()], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL
    )
    try:
        for _ in range(100):
            if proc.poll() is not None:
                raise RuntimeError(
                    f"server exited with {proc.returncode} before listening on {port}"
                )
            if _port_open(port):
                break
            time.sleep(0.1)
        else:
            raise RuntimeError("blank server did not start in time")
        yield f"http://127.0.0.1:{port}"
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()


@pytest.fixture()
def blank_page(browser, blank_server, request):
    viewport = getattr(request, "param", PHONE)
    ctx = browser.new_context(viewport=viewport)
    pg = ctx.new_page()
    yield pg
    ctx.close()


@pytest.mark.parametrize(
    "blank_page", [PHONE, DESKTOP], indirect=True, ids=["phone-390", "desktop"]
)
def test_first_run_checklist_shows_with_no_dead_end(blank_page, blank_server):
    page = blank_page
    page.goto(blank_server + "/#board")

    checklist = page.locator(".first-run")
    expect(checklist).to_be_visible()
    expect(checklist).to_contain_text("Get started")
    for label in (
        "Agent CLI on this server",
        "tmux ready",
        "git ready",
        "A project",
        "First session",
    ):
        expect(checklist.get_by_text(label, exact=True)).to_be_visible()

    # The single primary action stays on screen and inside the viewport at
    # every width this suite tests, including the narrowest phone.
    cta = checklist.locator(".first-run-cta")
    expect(cta).to_be_visible()
    expect(cta).to_have_text("Add your first machine")
    box = cta.bounding_box()
    viewport = page.viewport_size
    assert box is not None
    assert 0 <= box["x"] and box["x"] + box["width"] <= viewport["width"] + 1, box

    # No dead end: pressing it always opens a real, usable dialog — whatever
    # agent CLIs are or aren't installed on the machine running this test.
    cta.click()
    expect(page).to_have_url(blank_server + "/#targets")
    page.get_by_role("button", name="Add machine", exact=True).click()
    dialog = page.get_by_role("dialog", name="Add machine", exact=True)
    expect(dialog).to_be_visible()
    dialog.get_by_label("Machine name").fill("first-run-machine")
    dialog.get_by_label("Connection").select_option("local")
    dialog.get_by_role("button", name="Save machine").click()
    expect(dialog).not_to_be_visible()
    expect(checklist.locator(".first-run-cta")).to_have_text("Start your first session")
    checklist.locator(".first-run-cta").click()
    expect(page.get_by_role("dialog", name="New session", exact=True)).to_be_visible()
    targets = page.request.get(blank_server + "/api/targets").json()
    target = next(t for t in targets if t["name"] == "first-run-machine")
    assert page.request.delete(blank_server + f"/api/targets/{target['id']}").ok


def test_first_run_checklist_hides_once_something_exists(blank_page, blank_server):
    page = blank_page
    page.goto(blank_server + "/#board")
    expect(page.locator(".first-run")).to_be_visible()

    target = page.request.post(
        blank_server + "/api/targets",
        data={"name": "local", "kind": "local"},
    ).json()
    page.request.post(
        blank_server + "/api/projects",
        data={"name": "myrepo", "target_id": target["id"], "repo_path": "/tmp/myrepo"},
    )

    page.reload()
    expect(page.locator(".first-run")).to_have_count(0)
