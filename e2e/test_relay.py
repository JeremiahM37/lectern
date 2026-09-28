"""End-to-end encrypted relay (docs/relay.md), in a real browser.

A real `lectern relay` process and an isolated Lectern (its own port, DB,
HOME and tmux directory, token auth so nothing gets in without a credential)
connected to it. The owner pairs a phone from Settings; the phone, holding no
token or cookie at all, then runs the whole approval loop through the relay.
Every frame the phone's browser exchanged with the relay is checked for
plaintext.
"""
import os
import re
import secrets
import subprocess
import tempfile
import time
from pathlib import Path

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE, ROOT, _binary, _port_open, _start, _stop, _unused_port
from test_terminal_workspace import real_terminal  # noqa: F401 (fixture)

OWNER_TOKEN = "relayowner-" + secrets.token_hex(8)
HOST_SECRET = secrets.token_hex(32)
TITLE = "Relay gated deploy " + secrets.token_hex(4)


TERMINAL_RELAY_PORT = _unused_port()


def _start_relay(relay_port):
    log = Path(tempfile.mkdtemp(prefix="lec-relay-")) / "relay.log"
    with log.open("wb") as out:
        relay = subprocess.Popen([_binary(), "relay", "--listen", f"127.0.0.1:{relay_port}"], cwd=ROOT,
                                 env={**os.environ, "LECTERN_RELAY_HOST_SECRET": HOST_SECRET},
                                 stdout=out, stderr=subprocess.STDOUT)
    for _ in range(100):
        if _port_open(relay_port):
            return relay
        time.sleep(0.1)
    relay.kill()
    raise RuntimeError("relay did not start: " + log.read_text(errors="replace"))


def _watch_relay_frames(page, relay_port, frames):
    def watch(ws):
        if f":{relay_port}/" in ws.url:
            ws.on("framesent", lambda payload: frames.append(("sent", payload)))
            ws.on("framereceived", lambda payload: frames.append(("received", payload)))
    page.on("websocket", watch)


def _assert_no_plaintext(frames, needles):
    assert len(frames) > 10, f"only {len(frames)} relay frames seen"
    for direction, payload in frames:
        raw = payload.encode() if isinstance(payload, str) else payload
        for needle in needles:
            assert needle.encode() not in raw, f"relay saw {needle!r} in a {direction} frame"


@pytest.fixture(scope="module")
def relay_stack():
    relay_port = _unused_port()
    relay = _start_relay(relay_port)
    port = _unused_port()
    # LECTERN_AUTH=token: the phone must get nothing over plain HTTP, so the
    # only way it can work at all is the relay (see test_device_pairing.py
    # for why auto-detection is avoided in fixtures).
    lectern = _start(port, {"LECTERN_AUTH": "token", "LECTERN_AUTH_TOKEN": OWNER_TOKEN,
                            "LECTERN_RELAY_URL": f"ws://127.0.0.1:{relay_port}",
                            "LECTERN_RELAY_HOST_SECRET": HOST_SECRET})
    try:
        yield f"http://127.0.0.1:{port}", relay_port
    finally:
        _stop(lectern, port)
        relay.terminate()
        relay.wait(timeout=10)


def _open_devices(page):
    if page.get_by_role("button", name="Show navigation", exact=True).is_visible():
        page.get_by_role("button", name="Show navigation", exact=True).click()
    page.locator('.tab[data-tab="settings"]').click()
    page.get_by_role("tab", name="Phone & devices").click()


def _tab(page, name):
    # Pages renamed in the simpler navigation (docs/design/simple-ui.md).
    legacy = name
    name = {"board": "tasks", "deck": "overview", "targets": "settings"}.get(name, name)
    if page.get_by_role("button", name="Show navigation", exact=True).is_visible():
        page.get_by_role("button", name="Show navigation", exact=True).click()
    button = page.locator(f'.tab[data-tab="{name}"]')
    if not button.is_visible():
        page.locator("#nav-overflow > summary").click()
        page.locator(f'[data-nav-target="{name}"]').click()
    else:
        button.click()
    if legacy == "targets":
        # The old Settings page opened on Machines.
        page.locator('[data-settings="machines"]').click()


def test_approval_round_trip_over_the_relay(browser, relay_stack):
    base, relay_port = relay_stack
    owner_ctx = browser.new_context(viewport=DESKTOP)
    owner = owner_ctx.new_page()
    phone_ctx = browser.new_context(viewport=PHONE)
    phone = phone_ctx.new_page()
    frames = []
    _watch_relay_frames(phone, relay_port, frames)
    try:
        owner.goto(base)
        owner.evaluate(f"localStorage.setItem('lec-token', '{OWNER_TOKEN}')")
        owner.reload()
        _open_devices(owner)
        panel = owner.get_by_test_id("relay-panel")
        expect(panel.get_by_test_id("relay-state")).to_contain_text("Connected to", timeout=15000)
        panel.get_by_role("button", name="Pair a phone over the relay").click()
        link = panel.locator("a.relay-pair-link")
        expect(link).to_be_visible(timeout=10000)
        pair_url = link.get_attribute("href")
        assert pair_url and "/relay-pair#p=" in pair_url

        # The phone has no credential: plain HTTP is refused.
        assert phone_ctx.request.get(base + "/api/approvals").status == 401

        phone.goto(pair_url)
        expect(phone.get_by_test_id("relay-pair")).to_be_visible()
        # The secrets are dropped from the address bar right away.
        expect(phone).to_have_url(re.compile(r".*/relay-pair$"))
        phone.fill("#relay-pair-name", "Relay test phone")
        phone.click("#relay-pair-submit")

        # Lands in the real app, working entirely through the relay.
        expect(phone.locator("#conn-label")).to_have_text("LIVE", timeout=20000)
        who = phone.evaluate("async () => (await fetch('/api/whoami')).json()")
        assert who["kind"] == "relay-device" and who["human"] is True, who

        # The whole approval loop, from the phone, through the relay. The
        # navigation is served by the pinned service worker, not the network.
        phone.goto(base + "/#board")
        response = phone.reload()
        assert response is not None and response.from_service_worker, "navigation was not served by the pinned worker"
        expect(phone.locator("#conn-label")).to_have_text("LIVE", timeout=20000)
        phone.click("#fab")
        phone.fill("#f-title", TITLE)
        phone.fill("#f-prompt", "deploy [mock:approval]")
        phone.select_option("#f-perm", "default")
        phone.click("#f-go")
        expect(phone.locator("#appr-badge:visible, #more-badge:visible")).to_be_visible(timeout=20000)
        _tab(phone, "approvals")
        row = phone.locator(".rowcard", has_text="Bash").first
        expect(row).to_be_visible()
        expect(row.locator("pre")).to_contain_text("rm -rf build/")
        row.locator("button:has-text('Allow once')").first.click()
        _tab(phone, "board")
        expect(phone.locator(".col.s-review .card", has_text=TITLE)).to_be_visible(timeout=20000)

        # The pinned worker refuses a shell that differs from what this
        # Lectern signed: here, anyone on the install path swapping one file.
        phone_ctx.route("**/icon.svg", lambda route: route.fulfill(body="<svg>evil</svg>", content_type="image/svg+xml"))
        verdict = phone.evaluate("""() => new Promise(async (resolve) => {
            const reg = await navigator.serviceWorker.ready;
            const ch = new MessageChannel();
            ch.port1.onmessage = (e) => resolve(e.data);
            reg.active.postMessage({type: 'lec-relay-pinned'}, [ch.port2]);
        })""")
        phone_ctx.unroute("**/icon.svg")
        assert verdict["ok"] is False and "does not match" in verdict["error"], verdict

        # Everything crossed the relay, and none of it was readable there.
        _assert_no_plaintext(frames, (TITLE, "rm -rf build", "/api/", "approved", "relay-device", "Relay test phone", "mock:approval"))

        # The owner sees the device and revokes it; the phone is cut off.
        owner.reload()
        _open_devices(owner)
        device = owner.get_by_test_id("relay-device")
        expect(device).to_have_count(1, timeout=10000)
        expect(device).to_contain_text("Relay test phone")
        owner.once("dialog", lambda d: d.accept())
        device.get_by_role("button", name="Revoke").click()
        expect(owner.get_by_test_id("relay-device")).to_have_count(0, timeout=10000)
        expect(phone.locator('.relay-banner[data-status="revoked"]')).to_be_visible(timeout=20000)
    finally:
        phone_ctx.close()
        owner_ctx.close()


def test_relay_serves_no_pages_or_scripts(relay_stack):
    import urllib.error
    import urllib.request
    _, relay_port = relay_stack
    for path in ("/", "/relay-pair", "/sw.js", "/react/assets/app.js"):
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{relay_port}{path}", timeout=5)
            raise AssertionError(f"relay served {path}")
        except urllib.error.HTTPError as err:
            assert err.code == 404 and err.read() == b"", path


@pytest.fixture(scope="module")
def terminal_relay():
    relay = _start_relay(TERMINAL_RELAY_PORT)
    try:
        yield TERMINAL_RELAY_PORT
    finally:
        relay.terminate()
        relay.wait(timeout=10)


@pytest.mark.parametrize("real_terminal", [{"env": {
    "LECTERN_RELAY_URL": f"ws://127.0.0.1:{TERMINAL_RELAY_PORT}", "LECTERN_RELAY_HOST_SECRET": HOST_SECRET}}], indirect=True)
def test_a_real_terminal_over_the_relay(browser, terminal_relay, real_terminal):
    """The terminal's WebSocket (ttyd behind /term/) runs through the tunnel."""
    t = real_terminal
    for _ in range(100):
        if t["api"]("/relay").get("connected"):
            break
        time.sleep(0.1)
    else:
        raise AssertionError("Lectern never reached the relay")
    minted = t["api"]("/relay/pair", {})
    ctx = browser.new_context(viewport=PHONE)
    phone = ctx.new_page()
    frames = []
    _watch_relay_frames(phone, terminal_relay, frames)
    sockets = []
    phone.on("websocket", lambda ws: sockets.append(ws.url))
    marker = "RELAY-TERMINAL-" + secrets.token_hex(4)
    try:
        phone.goto(t["url"] + "/relay-pair#p=" + minted["fragment"])
        phone.click("#relay-pair-submit")
        expect(phone.locator("#conn-label")).to_have_text("LIVE", timeout=20000)
        phone.goto(f"{t['url']}/terminal/session/{t['id']}")
        expect(phone.locator("#connection")).to_have_text("Connected", timeout=20000)
        expect(phone.locator("#agent-terminal .xterm-screen")).to_contain_text("$", timeout=10000)
        phone.locator("#agent-terminal").click()
        phone.keyboard.type("echo " + marker, delay=1)
        phone.keyboard.press("Enter")
        expect(phone.locator("#agent-terminal .xterm-screen")).to_contain_text(marker, timeout=10000)
        _assert_no_plaintext(frames, (marker, "/term/", "echo "))
        # The terminal never opened a socket to Lectern itself: only the relay.
        assert sockets and all(f":{terminal_relay}/" in u for u in sockets), sockets
    finally:
        ctx.close()
