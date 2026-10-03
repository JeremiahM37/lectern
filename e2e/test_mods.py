"""Mods (docs/mods.md) in the real browser: a plugin's mod installed through
consent draws a status line, a badge on a session card and a pane whose
state survives a reload, adds a palette command, and rewrites or refuses a
message sent to a session before it leaves the page."""
import re
from pathlib import Path

import pytest
from playwright.sync_api import expect

from test_terminal_workspace import real_terminal  # noqa: F401  (fixture)

EXAMPLE = str(Path(__file__).resolve().parents[1] / "examples" / "plugins" / "mod-demo")
ALLOW = {"env": {"LECTERN_PLUGINS_ALLOW_UNAUTHENTICATED": "1"}}


def install(page, url):
    preview = page.request.post(f"{url}/api/plugins/preview", data={"kind": "path", "path": EXAMPLE})
    assert preview.ok, preview.text()
    pv = preview.json()
    accept = sorted(c["key"] if not c.get("detail") else f'{c["key"]}: {d}'
                    for c in pv["capabilities"] for d in (c.get("detail") or [None]))
    assert any(a.startswith("mods: demo (mods/demo.js)") for a in accept), accept
    done = page.request.post(f"{url}/api/plugins/install", data={"hash": pv["hash"], "accept": accept})
    assert done.ok, done.text()


def palette(page, text):
    page.keyboard.press("Control+k")
    page.get_by_role("combobox", name="Search sessions, tasks, and actions").fill(text)
    page.get_by_role("dialog", name="Search Lectern").get_by_role("option", name=re.compile(text)).click()


@pytest.mark.parametrize("real_terminal", [ALLOW], indirect=True)
def test_a_mod_draws_hooks_and_keeps_state(page, real_terminal):
    t = real_terminal
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    page.set_viewport_size({"width": 1440, "height": 900})
    install(page, t["url"])
    page.goto(t["url"] + "/#sessions")

    # Status line and a badge on the session's card.
    expect(page.locator("[data-mod-status]")).to_contain_text("demo mod on")
    card = page.locator(f'.scard[data-session-id="{t["id"]}"]')
    expect(card.locator(".scard-mods .mod-badge")).to_have_text(re.compile(rf"#{t['id']} "))

    # A palette command opens its pane; the counter's state survives a reload.
    palette(page, "Mod demo: open the counter")
    pane = page.locator('[data-mod-pane="example.mod-demo/demo/counter"]')
    expect(pane).to_contain_text("Pressed 0 times")
    pane.get_by_role("button", name="Press").click()
    expect(pane).to_contain_text("Pressed 1 times")
    pane.get_by_role("button", name="Press").click()
    expect(pane).to_contain_text("Pressed 2 times")
    page.reload()
    expect(page.locator("[data-mod-status]")).to_contain_text("demo mod on")
    palette(page, "Mod demo: open the counter")
    expect(pane).to_contain_text("Pressed 2 times")
    pane.get_by_role("button", name="Close").click()
    expect(pane).to_have_count(0)

    # prompt.submit: "shout:" is rewritten before it is sent; a wipe is refused.
    sent = []
    page.on("request", lambda r: sent.append(r.post_data_json) if r.method == "POST" and r.url.endswith(f"/sessions/{t['id']}/send") else None)
    card.get_by_text("Chat").click()
    box = page.locator("#conversation-input")
    box.fill("shout: echo modded")
    page.get_by_text("Send", exact=True).click()
    page.wait_for_timeout(500)
    assert {"text": "ECHO MODDED"}.items() <= (sent[-1] or {}).items(), sent
    count = len(sent)
    box.fill("rm -rf /")
    page.get_by_text("Send", exact=True).click()
    expect(page.get_by_text("that would delete the whole disk").first).to_be_visible()
    assert len(sent) == count, sent

    # Settings → Plugins shows the mod running in this browser.
    page.get_by_role("button", name="Close conversation").click()
    page.goto(t["url"] + "/#settings/plugins")
    plugin = page.locator('[data-plugin="example.mod-demo"]')
    plugin.get_by_role("button", name="Details").click()
    expect(plugin.locator('[data-mods-here] [data-mod-state="running"]')).to_contain_text("demo")

    # Turned off, it is gone: no status, no badge.
    plugin.get_by_role("button", name="Turn off").click()
    page.goto(t["url"] + "/#sessions")
    expect(card).to_be_visible()
    expect(page.locator("[data-mod-status]")).to_have_count(0)
    expect(card.locator(".scard-mods")).to_have_count(0)
    assert not errors, errors


@pytest.mark.parametrize("real_terminal", [ALLOW], indirect=True)
def test_a_mod_cannot_reach_the_network_or_the_page(page, real_terminal, tmp_path):
    """The sandbox: no fetch, no parent page, no cookies — only $."""
    t = real_terminal
    plugin = tmp_path / "probe"
    (plugin / "mods").mkdir(parents=True)
    (plugin / "lectern-plugin.yaml").write_text(
        "id: example.probe\nname: Probe\nversion: 0.1.0\ncapabilities: {mods: [web]}\n"
        "contributes: {mods: [{id: probe, path: mods/probe.js, surfaces: [web]}]}\n")
    (plugin / "mods" / "probe.js").write_text("""
export function register(on) {
  on("app.start", async ($, e, next) => {
    const tries = [];
    try { await fetch("/api/sessions"); tries.push("fetch ok"); } catch { tries.push("fetch blocked"); }
    try { parent.document.title; tries.push("page ok"); } catch { tries.push("page blocked"); }
    try { document.cookie; tries.push("cookie " + (document.cookie ? "read" : "empty")); } catch { tries.push("cookie blocked"); }
    try { await $.api.get("/api/sessions"); tries.push("api ok"); } catch (err) { tries.push("api refused"); }
    $.ui.status(tries.join(", "));
    return next(e);
  });
}
""")
    preview = page.request.post(f"{t['url']}/api/plugins/preview", data={"kind": "path", "path": str(plugin)}).json()
    accept = sorted(c["key"] if not c.get("detail") else f'{c["key"]}: {d}' for c in preview["capabilities"] for d in (c.get("detail") or [None]))
    assert page.request.post(f"{t['url']}/api/plugins/install", data={"hash": preview["hash"], "accept": accept}).ok
    page.goto(t["url"] + "/#sessions")
    status = page.locator("[data-mod-status]")
    expect(status).to_contain_text("fetch blocked, page blocked")
    expect(status).not_to_contain_text("cookie read")
    # Without the api capability, $.api is refused.
    expect(status).to_contain_text("api refused")
