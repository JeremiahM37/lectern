"""First run (docs/design/simple-ui.md): a brand-new install opens on
Sessions with one primary action, "Start an agent", and that action always
leads somewhere real — even with no machine registered yet ("no dead ends").

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
def test_first_run_is_one_action_with_no_dead_end(blank_page, blank_server):
    page = blank_page
    page.goto(blank_server)

    start = page.locator("#getting-started")
    expect(start).to_be_visible()
    expect(start).to_contain_text("Start your first agent")
    # No checklist to tick: only what is actually missing is listed, each
    # with its fix, and a project is not a prerequisite.
    expect(start).not_to_contain_text("A project")
    for row in start.locator(".gs-problems li").all():
        expect(row.locator("code")).to_have_count(1)
    # With no agent installed, a demo agent is offered instead.
    if start.locator(".gs-problems li[data-check='agent']").count():
        expect(page.locator("#gs-demo")).to_be_visible()

    # The single primary action is on screen at every width.
    cta = page.locator("#gs-start")
    expect(cta).to_have_text("Start an agent")
    box = cta.bounding_box()
    assert box is not None
    assert 0 <= box["x"] and box["x"] + box["width"] <= page.viewport_size["width"] + 1, box
    assert box["y"] + box["height"] <= page.viewport_size["height"], box

    # No dead end: with no machine at all, choosing a folder adds this
    # computer and browses it.
    cta.click()
    sheet = page.get_by_role("dialog", name="Start an agent", exact=True)
    expect(sheet).to_be_visible()
    expect(sheet.locator("#ns-ask")).to_be_checked()
    sheet.locator("#ns-browse").click()
    picker = page.get_by_role("dialog", name="Choose a folder", exact=True)
    expect(picker).to_be_visible()
    expect(picker.locator("#folder-path")).to_have_text("~")
    bounds = picker.bounding_box()
    assert bounds["x"] >= 0 and bounds["x"] + bounds["width"] <= page.viewport_size["width"] + 1, bounds
    picker.locator("#folder-empty").click()
    expect(picker).to_have_count(0)
    expect(sheet.locator("#ns-project")).to_have_value("")
    targets = page.request.get(blank_server + "/api/targets").json()
    assert [t["kind"] for t in targets] == ["local"], targets
    assert page.request.delete(blank_server + f"/api/targets/{targets[0]['id']}").ok


def test_a_project_alone_does_not_hide_first_run(blank_page, blank_server):
    """The old checklist vanished the moment `lectern up` registered a project,
    so its "Start your first session" pointed at a button that no longer
    existed. First run now lasts until there is a session."""
    page = blank_page
    target = page.request.post(
        blank_server + "/api/targets",
        data={"name": "local", "kind": "local"},
    ).json()
    page.request.post(
        blank_server + "/api/projects",
        data={"name": "myrepo", "target_id": target["id"], "repo_path": "/tmp/myrepo"},
    )
    page.goto(blank_server)
    expect(page.locator("#getting-started")).to_be_visible()
    page.locator("#gs-start").click()
    sheet = page.get_by_role("dialog", name="Start an agent", exact=True)
    expect(sheet.locator("#ns-project")).to_have_value(str(page.request.get(blank_server + "/api/projects").json()[0]["id"]))
