"""Orchestrate: describe a task in the quick bar and Lectern runs the whole
delegated-build loop; the same switch sits in the New task sheet. The React
harness checks the request shape; the real server (mock mode) checks that an
orchestrated task is a lead with the label and the guide."""
from pathlib import Path
import subprocess, time, urllib.request
import pytest
from playwright.sync_api import expect
from conftest import DESKTOP, PHONE

ROOT = Path(__file__).parents[1]


def test_quickbar_orchestrate_mode_and_form_switch(browser):
    p = subprocess.Popen(["npm", "exec", "vite", "--", "--host", "127.0.0.1", "--port", "4189"], cwd=ROOT / "frontend",
                         stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT)
    try:
        for _ in range(50):
            try:
                urllib.request.urlopen("http://127.0.0.1:4189/react/board-harness.html")
                break
            except Exception:
                time.sleep(.1)
        with browser.new_context() as context:
            page = context.new_page()
            page.goto("http://127.0.0.1:4189/react/board-harness.html")
            page.get_by_text("Backlog job").wait_for()
            mode = page.locator("#qb-mode")
            expect(mode.get_by_role("radio", name="Dispatch")).to_have_attribute("aria-checked", "true")
            # Plain dispatch sends no orchestrate flag.
            page.fill("#qb-input", "bump the version")
            page.press("#qb-input", "Enter")
            page.wait_for_function("window.calls.some(x => x[0] === 'create' && x[1].title === 'bump the version')")
            assert page.evaluate("window.calls.find(x => x[0] === 'create' && x[1].title === 'bump the version')[1].orchestrate") is None
            # Orchestrate: the bar changes its look, its placeholder and its hint, and ⏎ sends orchestrate:true.
            mode.get_by_role("radio", name="Orchestrate").click()
            expect(page.locator("#quickbar")).to_have_class("orchestrate")
            expect(page.locator("#qb-input")).to_have_attribute("placeholder", "Describe the outcome, hit ⏎ — Lectern plans, builds and reviews it")
            expect(page.locator("#qb-orch-hint")).to_contain_text("flash-builder")
            page.fill("#qb-input", "add an /opds/recent feed")
            page.press("#qb-input", "Enter")
            page.wait_for_function("window.calls.some(x => x[0] === 'create' && x[1].orchestrate === true)")
            assert page.evaluate("window.calls.find(x => x[0] === 'create' && x[1].orchestrate === true)[1].title") == "add an /opds/recent feed"
            expect(page.locator(".card", has_text="add an /opds/recent feed").locator(".chip.orch")).to_have_text("✦ orchestrated")
            # The choice survives a reload.
            page.reload()
            page.get_by_text("Backlog job").wait_for()
            expect(page.locator("#qb-mode").get_by_role("radio", name="Orchestrate")).to_have_attribute("aria-checked", "true")
            # The full form has the same switch, big, and only Claude Code or Codex can lead.
            page.locator("#qb-orch-hint").get_by_role("button", name="Open the full form").click()
            row = page.locator("#f-orchestrate")
            expect(row).to_be_visible()
            size = page.evaluate("getComputedStyle(document.querySelector('#f-orchestrate > span > b')).fontSize")
            assert float(size.rstrip("px")) >= 16
            row.get_by_role("switch", name="Orchestrate").click()
            expect(row).to_have_class("orchestrate-row on")
            expect(page.locator('#f-agent button[data-agent="flash-builder"]')).to_be_disabled()
            expect(page.locator('#f-agent button[data-agent="codex"]')).to_be_enabled()
            page.get_by_label("Title").fill("Orchestrated feature")
            page.get_by_role("button", name="Dispatch to board").click()
            page.wait_for_function("window.calls.some(x => x[0] === 'create' && x[1].title === 'Orchestrated feature' && x[1].orchestrate === true)")
    finally:
        p.terminate()
        p.wait(timeout=5)


@pytest.mark.parametrize("page", [PHONE, DESKTOP], indirect=True, ids=["phone390", "desktop1440"])
def test_orchestrated_task_is_a_lead_on_the_real_server(page, server):
    def reset():
        page.request.put(f"{server}/api/delegation", data={"enabled": False, "lead_agent": "", "lead_model": ""})
        agents = page.request.get(f"{server}/api/agents").json()
        page.request.put(f"{server}/api/agents", data=[a for a in agents if not a.get("builtin") and a["name"] != "flash-builder"])

    reset()
    page.goto(server + "/#board")
    page.locator("#qb-mode").get_by_role("radio", name="Orchestrate").click()
    # Off: the bar says why instead of failing on ⏎.
    expect(page.locator("#qb-orch-hint")).to_have_class("off")
    expect(page.locator("#qb-orch-hint")).to_contain_text("Delegated builds ON")
    page.fill("#qb-input", "orchestrate while off")
    page.press("#qb-input", "Enter")
    expect(page.locator(".toast", has_text="Delegated builds ON")).to_be_visible(timeout=5000)
    assert not any(t["title"] == "orchestrate while off" for t in page.request.get(f"{server}/api/tasks").json())
    # On (preset worker, mock mode: nothing leaves the box).
    page.request.post(f"{server}/api/delegation/preset", data={"api_key": "sk-not-a-real-key"})
    page.request.put(f"{server}/api/delegation", data={"enabled": True, "lead_agent": "codex"})
    page.reload()
    page.locator("#qb-mode").get_by_role("radio", name="Orchestrate").click()
    expect(page.locator("#qb-orch-hint")).not_to_have_class("off")
    expect(page.locator("#qb-orch-hint")).to_contain_text("flash-builder")
    page.fill("#qb-input", "orchestrate: add a /health endpoint")
    page.press("#qb-input", "Enter")
    card = page.locator(".card", has_text="orchestrate: add a /health")
    expect(card).to_be_visible(timeout=10000)
    expect(card.locator(".chip.orch")).to_have_text("✦ orchestrated")
    task = next(t for t in page.request.get(f"{server}/api/tasks").json() if t["title"].startswith("orchestrate: add a /health"))
    assert task["agent"] == "codex" and "orchestrated" in task["labels"]
    assert task["prompt"].startswith("You are the LEAD") and "REQUEST:\n\norchestrate: add a /health endpoint" in task["prompt"]
    # Once the mock ran it, the attempt's launch carried the Lectern MCP server.
    for _ in range(100):
        t = page.request.get(f"{server}/api/tasks/{task['id']}").json()
        if t["status"] in ("review", "failed", "done"):
            break
        time.sleep(.2)
    page.request.delete(f"{server}/api/tasks/{task['id']}")
    reset()
