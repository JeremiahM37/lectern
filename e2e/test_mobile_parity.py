"""The phone gestures and states: the key row a person arranges, offline
data with a stale marker, pull to refresh, swipe actions and bottom sheets,
dictation transcribed on the host, and pairing links for the Android app.

Everything runs against real lectern binaries; only the whisper.cpp binary is
a stand-in (a real one needs a model download), and it records what it was
given so the WAV the phone produced is checked too.
"""
import json
import os
import re
import sqlite3
import subprocess
import tempfile
import threading
import time
import urllib.request
from pathlib import Path

import pytest
from playwright.sync_api import expect

from conftest import PHONE, _start, _stop, _unused_port
from test_terminal_workspace import real_terminal, capture  # noqa: F401  (fixture)
from test_mobile_terminal import attach

TOUCH_PHONE = {"viewport": PHONE, "has_touch": True, "is_mobile": True}


# ---- the key row -------------------------------------------------------------

def _hold(page, locator, ms):
    locator.scroll_into_view_if_needed()
    box = locator.bounding_box()
    page.mouse.move(box["x"] + box["width"] / 2, box["y"] + box["height"] / 2)
    page.mouse.down()
    page.wait_for_timeout(ms)
    page.mouse.up()


@pytest.mark.parametrize('page', [PHONE], indirect=True)
def test_the_key_row_is_the_operators_and_held_arrows_repeat(page, real_terminal):
    t = real_terminal
    f = attach(page, t)
    screen = f.locator('#agent-terminal .xterm-screen')
    f.locator('[data-terminal-key="edit"]').click()
    dialog = f.locator('#keybar-dialog')
    expect(dialog).to_be_visible()
    # Remove a key, move one, add a combination and a saved reply.
    dialog.get_by_role('button', name='Remove Send tilde').click()
    dialog.get_by_role('button', name='Move Send pipe left').click()
    dialog.locator('#combo-ctrl').check()
    dialog.locator('#combo-key').select_option('custom')
    dialog.locator('#combo-char').fill('u')
    dialog.locator('#combo-add').click()
    dialog.locator('[data-add-key="backspace"]').click()
    dialog.locator('[data-add-quick="continue"]').click()
    dialog.locator('[data-close]').click()
    keys = f.locator('#terminal-keybar button').evaluate_all('(b)=>b.map(x=>x.dataset.terminalKey)')
    assert 'tilde' not in keys and keys.index('pipe') < keys.index('dash'), keys
    assert keys[-6:-2] == ['snippets', 'find', 'C-u', 'backspace'] and keys[-2].startswith('quick:') and keys[-1] == 'edit', keys
    # The quick-command key is the server-stored command, not a copy.
    stored = page.request.get(t['url'] + '/api/ui/prefs').json()
    assert 'continue' in json.dumps(stored), stored
    # It is this device's arrangement and survives a reload.
    page.reload()
    f = attach(page, t)
    assert f.locator('#terminal-keybar button').evaluate_all('(b)=>b.map(x=>x.dataset.terminalKey)') == keys
    screen = f.locator('#agent-terminal .xterm-screen')

    # Ctrl-U from the row clears what was typed, as it does on a keyboard.
    f.locator('#agent-terminal').click()
    page.keyboard.type('echo GARBAGE')
    f.locator('[data-terminal-key="C-u"]').click()
    page.keyboard.type('echo CLEARED-$((6*7))')
    page.keyboard.press('Enter')
    expect(screen).to_contain_text('CLEARED-42', timeout=10000)
    assert 'GARBAGE' not in capture(t).split('CLEARED')[-1]

    # Holding the left arrow repeats it: the cursor walks back several places.
    digits = '1234567890' * 4
    page.keyboard.type('echo ' + digits)
    _hold(page, f.locator('[data-terminal-key="left"]'), 800)
    page.keyboard.type('Z')
    page.keyboard.press('Enter')
    # Where Z landed in the command line: several places back from the end.
    out = ''
    for _ in range(50):
        found = re.findall(r'echo ([\dZ]{%d})' % (len(digits) + 1), capture(t).replace('\n', ''))
        if found:
            out = found[-1]
            break
        time.sleep(0.2)
    assert 'Z' in out, capture(t)
    moved = len(out) - 1 - out.index('Z')
    assert out.replace('Z', '') == digits and moved >= 3, (out, moved)
    # A short tap moves once, not repeatedly.
    page.keyboard.type('echo abc')
    f.locator('[data-terminal-key="left"]').click()
    page.keyboard.type('Y')
    page.keyboard.press('Enter')
    expect(screen).to_contain_text('abYc', timeout=10000)

    # Reset brings the default row back.
    f.locator('[data-terminal-key="edit"]').click()
    f.locator('#keybar-reset').click()
    f.locator('#keybar-dialog [data-close]').click()
    assert f.locator('[data-terminal-key="tilde"]').count() == 1
    assert f.locator('[data-terminal-key="C-u"]').count() == 0


# ---- a server with sessions, approvals and a stand-in whisper.cpp -----------

@pytest.fixture(scope="module")
def phone_server():
    tmp = Path(tempfile.mkdtemp(prefix="lec-phone-"))
    heard = tmp / "heard.wav"
    whisper = tmp / "whisper-cli"
    whisper.write_text(
        "#!/bin/sh\n"
        "while [ $# -gt 0 ]; do [ \"$1\" = -f ] && cp \"$2\" " + str(heard) + "; shift; done\n"
        "echo ' [BLANK_AUDIO]'; echo ' Run the tests please.'\n"
    )
    whisper.chmod(0o755)
    model = tmp / "ggml-base.en.bin"
    model.write_text("x")
    port = _unused_port()
    db = tmp / "phone.db"
    proc = _start(port, {"LECTERN_DB": str(db), "LECTERN_WHISPER_BIN": str(whisper),
                         "LECTERN_WHISPER_MODEL": str(model), "LECTERN_APPROVAL_HOLD": "60"})
    try:
        yield {"url": f"http://127.0.0.1:{port}", "db": db, "heard": heard}
    finally:
        _stop(proc, port)


def _api(base, method, path, body=None):
    req = urllib.request.Request(base + "/api" + path, method=method,
                                 data=None if body is None else json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=20) as resp:
        raw = resp.read()
        return json.loads(raw) if raw else None


def _session(server, name, **extra):
    return _api(server["url"], "POST", "/sessions", {"name": name, "scratch": True, "agent": "claude", **extra})


def _ask_permission(server, session_id):
    """A held PermissionRequest hook, as Claude Code sends it; returns the
    thread and a dict that receives the answer."""
    conn = sqlite3.connect(str(server["db"]))
    token = conn.execute("SELECT hook_token FROM sessions WHERE id=?", (session_id,)).fetchone()[0]
    conn.close()
    answer = {}

    def run():
        req = urllib.request.Request(
            f"{server['url']}/api/hook/session/{session_id}/PermissionRequest",
            data=json.dumps({"hook_event_name": "PermissionRequest", "tool_name": "Bash",
                             "tool_input": {"command": "make deploy"}}).encode(),
            headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"}, method="POST")
        with urllib.request.urlopen(req, timeout=90) as resp:
            answer["body"] = json.loads(resp.read() or b"{}")

    thread = threading.Thread(target=run, daemon=True)
    thread.start()
    return thread, answer


def _swipe(locator, dx):
    locator.evaluate("""async (el, dx) => {
      const r = el.getBoundingClientRect(), x = r.left + r.width / 2, y = r.top + 30;
      const ev = (type, px) => el.dispatchEvent(new PointerEvent(type, {pointerId: 5, pointerType: 'touch', isPrimary: true,
        clientX: px, clientY: y, bubbles: true, cancelable: true}));
      ev('pointerdown', x);
      for (let i = 1; i <= 8; i++) { ev('pointermove', x + dx * i / 8); await new Promise(r => setTimeout(r, 16)); }
      ev('pointerup', x + dx);
    }""", dx)


def test_swiping_a_card_approves_or_archives_it(browser, phone_server):
    ctx = browser.new_context(**TOUCH_PHONE)
    page = ctx.new_page()
    try:
        waiting = _session(phone_server, "Swipe to approve", permission_mode="ask")
        _session(phone_server, "Swipe to archive")
        thread, answer = _ask_permission(phone_server, waiting["id"])
        page.goto(phone_server["url"] + "/#sessions")
        row = page.locator('[data-swipe-row="%d"]' % waiting["id"])
        expect(row.locator(".scard")).to_contain_text("make deploy", timeout=20000)
        # A short swipe springs back and does nothing.
        _swipe(row, 40)
        page.wait_for_timeout(400)
        assert thread.is_alive()
        # A long swipe right approves what the session is waiting on.
        _swipe(row, 220)
        thread.join(20)
        assert not thread.is_alive(), "the held hook was never answered"
        assert answer["body"]["hookSpecificOutput"]["decision"]["behavior"] == "allow", answer
        expect(page.locator(".toast")).to_contain_text("Approved Bash")
        # Left on a live session stops and archives it, after asking, with Undo.
        page.once("dialog", lambda d: d.accept())
        other = page.locator(".swipe-row", has_text="Swipe to archive")
        _swipe(other, -240)
        toast = page.locator(".toast", has_text="Stopped and archived")
        expect(toast).to_be_visible(timeout=10000)
        expect(toast.locator("button", has_text="Undo")).to_be_visible()
        expect(page.locator(".scard", has_text="Swipe to archive")).to_have_count(0, timeout=10000)
    finally:
        ctx.close()


def test_pull_to_refresh_and_menus_as_bottom_sheets(browser, phone_server):
    ctx = browser.new_context(**TOUCH_PHONE)
    page = ctx.new_page()
    try:
        _session(phone_server, "Pull me")
        page.goto(phone_server["url"] + "/#sessions")
        expect(page.locator(".scard", has_text="Pull me")).to_be_visible(timeout=15000)
        _session(phone_server, "Arrived later")
        page.wait_for_timeout(300)
        seen = []
        page.on("request", lambda r: seen.append(r.url) if "/api/sessions" in r.url else None)
        page.locator("#view").evaluate("""async (view) => {
          const target = view.querySelector('.sesshead') || view;
          const t = (y) => new Touch({identifier: 1, target, clientX: 200, clientY: y});
          const fire = (type, y) => target.dispatchEvent(new TouchEvent(type, {touches: type === 'touchend' ? [] : [t(y)],
            changedTouches: [t(y)], bubbles: true, cancelable: true}));
          fire('touchstart', 150);
          for (let y = 160; y <= 330; y += 20) { fire('touchmove', y); await new Promise(r => setTimeout(r, 16)); }
          fire('touchend', 330);
        }""")
        expect(page.locator(".scard", has_text="Arrived later")).to_be_visible(timeout=10000)
        assert seen, "the pull did not refresh anything"

        # The card's More menu opens from the bottom edge, full width.
        card = page.locator(".scard", has_text="Pull me")
        card.locator(".action-menu>summary").click()
        panel = card.locator(".action-menu-panel")
        expect(panel).to_be_visible()
        page.wait_for_timeout(400)  # the sheet slides up; measure where it settles
        box = panel.bounding_box()
        assert box["x"] == 0 and abs(box["width"] - PHONE["width"]) <= 1, box
        assert abs(box["y"] + box["height"] - PHONE["height"]) <= 1, box
        # Its dimmed backdrop closes it.
        page.mouse.click(200, 60)
        expect(panel).to_be_hidden()
    finally:
        ctx.close()


def test_last_known_sessions_show_offline_with_a_stale_marker(browser, phone_server):
    # No service worker: this is the Android app's situation (its WebView has
    # none), and it keeps the test about the data cache alone.
    ctx = browser.new_context(viewport=PHONE, service_workers="block")
    page = ctx.new_page()
    try:
        _session(phone_server, "Known before the tunnel")
        page.goto(phone_server["url"] + "/#sessions")
        expect(page.locator(".scard", has_text="Known before the tunnel")).to_be_visible(timeout=15000)
        expect(page.locator("#offline-banner")).to_have_count(0)
        # The network goes: every API request fails.
        page.route("**/api/**", lambda route: route.abort())
        page.reload()
        expect(page.locator(".scard", has_text="Known before the tunnel")).to_be_visible(timeout=15000)
        banner = page.locator("#offline-banner")
        expect(banner).to_contain_text("Offline")
        expect(banner).to_contain_text("showing what Lectern said")
        # Back online: Retry fetches again and the marker goes.
        page.unroute("**/api/**")
        banner.get_by_role("button", name="Retry").click()
        expect(page.locator("#offline-banner")).to_have_count(0, timeout=10000)
    finally:
        ctx.close()


# ---- dictation on the host --------------------------------------------------

def test_dictation_is_transcribed_on_the_lectern_host(browser, phone_server):
    session = _session(phone_server, "Dictation target")
    if True:
        # A fake microphone: Chromium's test tone, with the permission granted.
        b = browser.browser_type.launch(args=["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream"])
        ctx = b.new_context(viewport=PHONE, permissions=["microphone"])
        page = ctx.new_page()
        # No browser speech recognition here, like the Android app's WebView.
        page.add_init_script("delete window.SpeechRecognition; delete window.webkitSpeechRecognition;")
        try:
            page.goto(phone_server["url"] + "/#targets")
            page.get_by_role("tab", name="Notifications").click()
            voice = page.locator('[data-testid="voice-settings"]')
            expect(voice).to_contain_text("Available (model base.en)")
            expect(voice.locator('input[value="device"]')).to_be_disabled()

            page.goto(phone_server["url"] + "/#sessions")
            card = page.locator(".scard", has_text="Dictation target")
            card.get_by_role("button", name="Chat").first.click()
            box = page.locator("#conversation-input")
            box.fill("Please:")
            mic = page.locator("#conversation-mic")
            expect(mic).to_be_visible()
            mic.click()
            expect(mic).to_contain_text("Recording")
            page.wait_for_timeout(1200)
            mic.click()
            expect(box).to_have_value("Please: Run the tests please.", timeout=15000)
            expect(mic).to_have_text("🎙")
            # Nothing was sent on the person's behalf.
            assert not _api(phone_server["url"], "GET", f"/sessions/{session['id']}").get("last_prompt")
        finally:
            b.close()
    wav = phone_server["heard"].read_bytes()
    assert wav[:4] == b"RIFF" and wav[8:12] == b"WAVE", wav[:16]
    rate = int.from_bytes(wav[24:28], "little")
    channels = int.from_bytes(wav[22:24], "little")
    assert (rate, channels) == (16000, 1), (rate, channels)
    assert len(wav) > 44 + 16000, "about a second of audio should have been recorded"


# ---- pairing links ----------------------------------------------------------

def test_pairing_links_for_messages_and_the_android_app(browser, pairing_server):
    owner_ctx = browser.new_context(viewport=PHONE)
    owner = owner_ctx.new_page()
    try:
        owner.goto(pairing_server)
        owner.evaluate("localStorage.setItem('lec-token','pairsecret123')")
        owner.goto(pairing_server + "/#targets")
        owner.reload()
        owner.get_by_role("tab", name="Devices").click()
        owner.get_by_role("button", name="Pair a phone").click()
        mint = owner.locator('[data-testid="pairing-mint"]')
        expect(mint).to_be_visible()
        code = mint.locator(".pairing-code-text").inner_text().replace("-", "")
        app = mint.locator('[data-testid="app-pair-link"]')
        expected = "lectern://pair?origin=" + urllib.request.quote(pairing_server, safe="") + "&code="
        assert app.get_attribute("href").startswith(expected), app.get_attribute("href")
        assert app.get_attribute("href").upper().endswith(code)
        expect(mint.locator('[data-testid="copy-pair-link"]')).to_be_visible()
    finally:
        owner_ctx.close()

    # The https link opened in a phone's browser on Android offers the app.
    android = browser.new_context(viewport=PHONE, user_agent="Mozilla/5.0 (Linux; Android 14; Pixel 7) AppleWebKit/537.36 Chrome/148 Mobile Safari/537.36")
    phone = android.new_page()
    try:
        phone.goto(pairing_server + "/pair#code=ABCD1234")
        link = phone.locator('[data-testid="open-in-app"] a')
        href = link.get_attribute("href")
        assert href.startswith("intent://pair?origin=") and "code=ABCD1234" in href, href
        assert "package=io.github.jeremiahm37.lectern" in href and "S.browser_fallback_url=" in href
        # The code still lands in the form for the browser pairing, and leaves the address bar.
        expect(phone.locator("#pair-code")).to_have_value("ABCD1234")
        assert "#code" not in phone.url
    finally:
        android.close()
    desktop = browser.new_context()
    page = desktop.new_page()
    try:
        page.goto(pairing_server + "/pair#code=ABCD1234")
        expect(page.locator("#pair-code")).to_have_value("ABCD1234")
        expect(page.locator('[data-testid="open-in-app"]')).to_have_count(0)
    finally:
        desktop.close()
