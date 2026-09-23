"""Phone terminal experience: its own tab row, a body swipe over mouse
reporting, a keyboard that cannot cover the prompt, and a clean reconnect."""
import os
import re
import subprocess
import time

import pytest
from playwright.sync_api import expect

from test_terminal_swipe_tabs import _session_frame, _touch_path, _touch_swipe
from test_terminal_workspace import capture, real_terminal


def attach(page, t, name="Real terminal"):
    page.goto(t["url"] + "/#sessions")
    page.locator(".scard", has_text=name).get_by_role(
        "button", name="⌨ Attach", exact=True
    ).click()
    expect(page.locator(".terminal-tab", has_text=name)).to_have_attribute(
        "aria-selected", "true", timeout=15000
    )
    return page.frame_locator(f'iframe[src="/terminal/session/{t["id"]}?embed=1"]')


def pane_size(t):
    out = subprocess.check_output(
        ["tmux", "display-message", "-p", "-t", "=terminal-test:", "#{pane_width} #{pane_height}"],
        env=t["env"],
    ).decode().split()
    return int(out[0]), int(out[1])


def wait_for_pane_height(t, smaller_than):
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        cols, rows = pane_size(t)
        if rows < smaller_than:
            return cols, rows
        time.sleep(0.1)
    raise AssertionError(f"the PTY never shrank below {smaller_than}: {pane_size(t)}")


def kill_ttyd(t):
    children = subprocess.check_output(
        ["ps", "--ppid", str(t["proc"].pid), "-o", "pid=,comm="]
    ).decode().splitlines()
    pids = [int(line.split()[0]) for line in children if line.split()[1] == "ttyd"]
    assert pids, children
    for pid in pids:
        os.kill(pid, 15)
    return pids


# The phone keyboard overlays the frame instead of resizing the layout viewport,
# which no browser-automation API can open. This is the visual viewport the
# browser reports with the keys up.
FAKE_KEYBOARD = """(height)=>{
  const listeners={resize:[],scroll:[]};
  const stub={height,width:innerWidth,offsetTop:0,offsetLeft:0,scale:1,pageTop:0,pageLeft:0,
    addEventListener(type,fn){(listeners[type]=listeners[type]||[]).push(fn);},
    removeEventListener(type,fn){listeners[type]=(listeners[type]||[]).filter(f=>f!==fn);}};
  window.__lecRealVisualViewport=window.visualViewport;
  Object.defineProperty(window,'visualViewport',{configurable:true,value:stub});
  window.dispatchEvent(new Event('resize'));
}"""
RESTORE_VIEWPORT = """()=>{
  Object.defineProperty(window,'visualViewport',{configurable:true,value:window.__lecRealVisualViewport});
  window.dispatchEvent(new Event('resize'));
}"""

# A full-screen app that redraws on resize and keeps its input line on the last
# row of the PTY, the way an interactive agent's prompt does.
INPUT_APP = '''import os,select,signal,sys,termios,tty
state={'redraw':True,'typed':b''}
def winch(*_): state['redraw']=True
signal.signal(signal.SIGWINCH,winch)
fd=sys.stdin.fileno();saved=termios.tcgetattr(fd);tty.setraw(fd)
def draw():
    cols,rows=os.get_terminal_size()
    typed=state['typed'].decode('utf-8','replace')
    body=''.join('history %03d\\r\\n'%i for i in range(max(0,rows-2)))
    line=('INPUT> '+typed)[:max(1,cols)]
    os.write(1,('\\x1b[2J\\x1b[H'+body+'\\x1b[%d;1H\\x1b[K'%rows+line).encode())
try:
    while True:
        if state['redraw']:
            state['redraw']=False;draw()
        ready,_,_=select.select([0],[],[],0.05)
        if ready:
            data=os.read(0,64)
            if not data: break
            state['typed']+=data;draw()
finally:
    os.write(1,b'\\x1b[2J\\x1b[H')
    termios.tcsetattr(fd,termios.TCSADRAIN,saved)
'''

MOUSE_APP = '''import os,tty,sys
tty.setraw(sys.stdin.fileno())
os.write(1,b'\\x1b[?1049h\\x1b[?1000h\\x1b[?1006h\\x1b[2J\\x1b[HMOUSE-APP-READY')
while True:
 data=os.read(0,4096)
 with open('mouse-input.log','ab') as f:f.write(data)
 if b'[<64;' in data:os.write(1,b'\\x1b[3;1HSCROLLED-OLDER-CONTENT')
'''


def mouse_log(root):
    path = root / "mouse-input.log"
    return path.read_bytes() if path.exists() else b""


@pytest.mark.parametrize("width", [320, 390, 820])
def test_tab_titles_own_a_full_row_with_the_controls_below(page, real_terminal, width):
    t = real_terminal
    page.set_viewport_size({"width": width, "height": 844})
    f = attach(page, t)

    bar = page.locator(".terminal-tabbar").bounding_box()
    tabs = page.locator(".terminal-tablist").bounding_box()
    tab = page.locator(".terminal-tab", has_text="Real terminal")
    new = page.get_by_role("button", name="New terminal").bounding_box()
    search = page.get_by_role("button", name="Search sessions and actions").bounding_box()
    overflow = page.locator(".terminal-actions>summary").bounding_box()
    focus = page.locator(".terminal-focus").bounding_box()
    switch = page.get_by_role("button", name="Switch agent or model").bounding_box()
    for box in (bar, tabs, new, search, overflow, focus, switch):
        assert box, "a tab bar control is missing"
        assert box["x"] >= -1 and box["x"] + box["width"] <= width + 1, box

    # The names get the whole width to themselves, and none is truncated.
    assert abs(tabs["x"] - bar["x"]) <= 14, (bar, tabs)
    assert tabs["width"] >= bar["width"] - 30, (bar, tabs)
    assert tab.evaluate("(el)=>el.scrollWidth<=el.clientWidth+1"), "the title is clipped"
    # Every control shares the compact row under the names.
    row = [new, search, overflow, focus, switch]
    assert max(b["y"] for b in row) - min(b["y"] for b in row) <= 4, row
    assert min(b["y"] for b in row) >= tabs["y"] + tabs["height"] - 2, (tabs, row)
    assert page.evaluate("document.documentElement.scrollWidth<=innerWidth")

    # The controls still work from that row, and the terminal keeps real estate.
    page.get_by_role("button", name="Search sessions and actions").click()
    expect(page.get_by_role("dialog", name="Search Lectern")).to_be_visible()
    page.keyboard.press("Escape")
    page.locator(".terminal-actions>summary").click()
    expect(page.get_by_role("menuitem", name="Close this view")).to_be_visible()
    page.locator(".terminal-actions>summary").click()
    screen = f.locator("#agent-terminal").bounding_box()
    assert screen and screen["height"] > 500, screen
    expect(f.locator("#terminal-keybar")).to_be_visible()
    page.screenshot(path=f"/tmp/lectern-phone-tabs-{width}.png")


def test_a_swipe_over_a_mouse_reporting_tui_changes_tabs_and_keeps_scroll(page, real_terminal, tmp_path):
    t = real_terminal
    second_root = tmp_path / "second-workspace"
    second_root.mkdir()
    subprocess.run(["tmux", "new-session", "-d", "-s", "terminal-two",
                    "-c", str(second_root), "bash --norc"], env=t["env"], check=True)
    second = t["api"]("/sessions/adopt", {
        "target_id": t["target_id"], "tmux_session": "terminal-two",
        "workdir": str(second_root), "name": "Second terminal", "agent": "claude",
    })
    page.set_viewport_size({"width": 390, "height": 844})
    attach(page, t, "Real terminal")
    page.goto(t["url"] + "/#sessions")
    page.locator(".scard", has_text="Second terminal").get_by_role(
        "button", name="⌨ Attach", exact=True
    ).click()
    expect(page.locator(".terminal-tab", has_text="Second terminal")).to_have_attribute(
        "aria-selected", "true", timeout=15000
    )
    frame = _session_frame(page, second["id"])
    box = page.locator(".terminal-tabpanel:not([hidden]) iframe").bounding_box()
    assert box and box["width"] > 250 and box["height"] > 300

    # A full-screen app owns the mouse: a vertical drag must still reach the
    # app's scrolling protocol, and a swipe must still reach the tab bar.
    (second_root / "mouse-app.py").write_text(MOUSE_APP)
    subprocess.run(["tmux", "send-keys", "-t", "=terminal-two:",
                    "python3 -u mouse-app.py", "Enter"], env=t["env"], check=True)
    expect(frame.locator(".xterm-screen")).to_contain_text("MOUSE-APP-READY", timeout=15000)
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if frame.evaluate("window.__lecTerminalState?.().mouseTrackingMode") != "none":
            break
        page.wait_for_timeout(100)
    assert frame.evaluate("window.__lecTerminalState?.().mouseTrackingMode") != "none"

    x, y = box["x"] + box["width"] / 2, box["y"] + box["height"] / 2
    _touch_path(page, [(x, y), (x, y + 20), (x, y + 55), (x, y + 90)])
    deadline = time.monotonic() + 3
    while time.monotonic() < deadline and b"[<64;" not in mouse_log(second_root):
        page.wait_for_timeout(50)
    assert b"[<64;" in mouse_log(second_root), "a vertical touch drag never reached the app"
    expect(page.locator('.terminal-tab[aria-selected="true"]')).to_have_text(
        re.compile("Second terminal")
    )

    # Two fingers are a pinch, not a tab change.
    cdp = page.context.new_cdp_session(page)
    cdp.send("Input.dispatchTouchEvent", {"type": "touchStart", "touchPoints": [
        {"x": x - 60, "y": y, "id": 1}, {"x": x + 60, "y": y, "id": 2}]})
    cdp.send("Input.dispatchTouchEvent", {"type": "touchMove", "touchPoints": [
        {"x": x + 90, "y": y, "id": 1}, {"x": x + 210, "y": y, "id": 2}]})
    cdp.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})
    cdp.detach()
    expect(page.locator('.terminal-tab[aria-selected="true"]')).to_have_text(
        re.compile("Second terminal")
    )

    # A gesture that starts on the key row sends no key and changes no tab.
    key = frame.locator('[data-terminal-key="escape"]').bounding_box()
    assert key
    before = mouse_log(second_root)
    _touch_swipe(page, key["x"] + key["width"] / 2, key["y"] + key["height"] / 2,
                 key["x"] + key["width"] / 2 + 140, key["y"] + key["height"] / 2)
    page.wait_for_timeout(600)
    assert mouse_log(second_root) == before, "a key row swipe typed into the terminal"
    expect(page.locator('.terminal-tab[aria-selected="true"]')).to_have_text(
        re.compile("Second terminal")
    )

    # One deliberate horizontal flick crosses to the other terminal...
    wheels = mouse_log(second_root).count(b"[<64;")
    _touch_swipe(page, box["x"] + box["width"] * .25, box["y"] + box["height"] * .5,
                 box["x"] + box["width"] * .65, box["y"] + box["height"] * .5)
    expect(page.locator(".terminal-tab", has_text="Real terminal")).to_have_attribute(
        "aria-selected", "true", timeout=3000
    )
    # ...and the app it left still owns its screen and got no wheel reports.
    expect(frame.locator(".xterm-screen")).to_contain_text("MOUSE-APP-READY")
    assert mouse_log(second_root).count(b"[<64;") == wheels
    page.screenshot(path="/tmp/lectern-phone-swipe-mouse.png")


def _prompt_row(frame, text):
    row = frame.locator("#agent-terminal .xterm-rows > div", has_text=text)
    expect(row).to_have_count(1, timeout=10000)
    return row.bounding_box()


def test_the_keyboard_shrinks_the_pty_and_leaves_the_prompt_above_it(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({"width": 390, "height": 844})
    (t["root"] / "input-app.py").write_text(INPUT_APP)
    f = attach(page, t)
    f.locator("#agent-terminal").click()
    subprocess.run(["tmux", "send-keys", "-t", "=terminal-test:", "python3 -u input-app.py", "Enter"],
                   env=t["env"], check=True)
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("INPUT>", timeout=15000)
    cols_before, rows_before = pane_size(t)
    assert rows_before > 20, (cols_before, rows_before)
    iframe = page.locator(".terminal-tabpanel:not([hidden]) iframe").bounding_box()
    assert iframe
    row = _prompt_row(f, "INPUT>")
    assert row["y"] + row["height"] > 400, "the fixture is not a full-height terminal"

    # The phone keyboard opens: the visible viewport gets shorter, the frame
    # does not. The terminal has to fit what a phone can see.
    visible = 420
    page.evaluate(FAKE_KEYBOARD, visible)
    expect(f.locator("body")).to_have_class(re.compile("fitted-viewport"), timeout=5000)
    cols_after, rows_after = wait_for_pane_height(t, rows_before)
    assert cols_after == cols_before, (cols_before, cols_after)
    fitted = f.locator("body").evaluate("()=>getComputedStyle(document.body).height")
    assert 200 < float(fitted.replace("px", "")) <= visible - iframe["y"], fitted

    # The live input line — and the cursor on it — stay above the keys.
    row = _prompt_row(f, "INPUT>")
    assert row["y"] + row["height"] <= visible + 2, (row, iframe)
    keybar = f.locator("#terminal-keybar").bounding_box()
    assert keybar and keybar["y"] + keybar["height"] <= visible + 2, (keybar, iframe)
    assert row["y"] + row["height"] <= keybar["y"] + 2, (row, keybar)
    f.locator("#agent-terminal").click()
    page.keyboard.type("HELLO-FROM-PHONE", delay=10)
    row = _prompt_row(f, "INPUT> HELLO-FROM-PHONE")
    assert row["y"] + row["height"] <= visible + 2, row
    page.screenshot(path="/tmp/lectern-phone-keyboard-open.png")

    # The keyboard closes: the terminal takes the screen back, unchanged.
    page.evaluate(RESTORE_VIEWPORT)
    expect(f.locator("body")).not_to_have_class(re.compile("fitted-viewport"), timeout=5000)
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline and pane_size(t)[1] < rows_before:
        time.sleep(0.1)
    assert pane_size(t) == (cols_before, rows_before), pane_size(t)
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("INPUT> HELLO-FROM-PHONE")


def test_a_standalone_terminal_fits_its_own_visual_viewport(page, real_terminal):
    from test_terminal_workspace import open_terminal
    t = real_terminal
    page.set_viewport_size({"width": 390, "height": 844})
    (t["root"] / "input-app.py").write_text(INPUT_APP)
    open_terminal(page, t)
    subprocess.run(["tmux", "send-keys", "-t", "=terminal-test:", "python3 -u input-app.py", "Enter"],
                   env=t["env"], check=True)
    expect(page.locator("#agent-terminal .xterm-screen")).to_contain_text("INPUT>", timeout=15000)
    _, rows_before = pane_size(t)
    page.evaluate(FAKE_KEYBOARD, 420)
    expect(page.locator("body")).to_have_class(re.compile("fitted-viewport"), timeout=5000)
    _, rows_after = wait_for_pane_height(t, rows_before)
    assert rows_after < rows_before
    row = page.locator("#agent-terminal .xterm-rows > div", has_text="INPUT>").bounding_box()
    assert row and row["y"] + row["height"] <= 422, row
    page.evaluate(RESTORE_VIEWPORT)
    expect(page.locator("body")).not_to_have_class(re.compile("fitted-viewport"), timeout=5000)


SOCKET_TRACKER = """window.terminalSockets=[];
  const Original=window.WebSocket;
  window.WebSocket=class extends Original {
    constructor(...args){super(...args);window.terminalSockets.push(this);}
  };"""


def _open_sockets(frame):
    return frame.locator("body").evaluate(
        "()=>window.terminalSockets? window.terminalSockets.filter(s=>s.readyState===1).length : -1"
    )


def test_offline_resume_keeps_the_session_and_never_replays_input(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({"width": 390, "height": 844})
    page.add_init_script(SOCKET_TRACKER)
    f = attach(page, t)
    f.locator("#agent-terminal").click()
    page.keyboard.type("printf 'BEFORE-OFFLINE-42\\n'")
    page.keyboard.press("Enter")
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("BEFORE-OFFLINE-42")
    # Output older than the viewport, so the screen after a resume proves the
    # session came back with its output rather than a blank terminal.
    page.keyboard.type("for i in $(seq 1 90); do echo MARK-$i-END; done")
    page.keyboard.press("Enter")
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("MARK-90-END")
    assert f.locator("body").evaluate(
        "()=>window.__lecTerminalState().bufferContains('MARK-45-END')"
    ), "the fixture never scrolled a marker out of the viewport"
    assert _open_sockets(f) == 1

    # The browser's own word that the network is gone: the view must say so at
    # once and drop the socket it can no longer use, without waiting for a
    # request to time out. The event is stubbed because a hermetic browser may
    # already report `navigator.onLine` as false and never fire it.
    f.locator("body").evaluate("()=>window.dispatchEvent(new Event('offline'))")
    expect(f.locator("#connection")).to_have_text("Offline", timeout=3000)
    expect(f.locator("#compact-status")).to_have_attribute("aria-label", "Terminal offline")
    assert _open_sockets(f) == 0, "the offline event left the dead stream open"
    f.locator("body").evaluate("()=>window.dispatchEvent(new Event('online'))")
    expect(f.locator("#connection")).to_have_text("Connected", timeout=15000)
    assert _open_sockets(f) == 1
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("MARK-90-END")

    # And the real thing: the phone loses its network and the stream dies.
    page.context.set_offline(True)
    kill_ttyd(t)
    expect(f.locator("#connection")).to_have_text("Offline", timeout=10000)
    expect(f.locator("#compact-status")).to_have_attribute("aria-label", "Terminal offline")
    # Keys typed while the phone has no network are dropped, never queued.
    page.keyboard.type("printf 'GHOST-OFFLINE-%s\\n' 99")
    page.keyboard.press("Enter")
    # Long enough that a backoff timer alone would miss the window below.
    page.wait_for_timeout(6000)
    page.context.set_offline(False)
    f.locator("body").evaluate("()=>window.dispatchEvent(new Event('online'))")
    expect(f.locator("#connection")).to_have_text("Connected", timeout=3000)
    expect(f.locator("#compact-status")).to_have_attribute("aria-label", "Terminal connected")
    assert _open_sockets(f) == 1, "a reconnect left more than one live terminal stream"
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("MARK-90-END")
    assert f.locator("body").evaluate(
        "()=>window.__lecTerminalState().bufferContains('MARK-45-END')"
    ), "the resume wiped the output that had scrolled out of the viewport"

    f.locator("#agent-terminal").click()
    page.keyboard.type("printf 'AFTER-RESUME-%s\\n' 7")
    page.keyboard.press("Enter")
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("AFTER-RESUME-7")
    out = capture(t)
    assert out.count("AFTER-RESUME-7") == 1, out
    assert "GHOST-OFFLINE-99" not in out, out
    assert "BEFORE-OFFLINE-42" in out, "the session did not survive the outage"
    page.screenshot(path="/tmp/lectern-phone-resume.png")


# Headless Chromium has no tab backgrounding, so the OS signal is stubbed and
# the page is told it went away and came back.
FAKE_BACKGROUND = """()=>{
  window.__lecForeground=false;
  Object.defineProperty(document,'hidden',{configurable:true,get:()=>!window.__lecForeground});
  Object.defineProperty(document,'visibilityState',{configurable:true,
    get:()=>window.__lecForeground?'visible':'hidden'});
  window.__lecSetForeground=(on)=>{window.__lecForeground=on;
    document.dispatchEvent(new Event('visibilitychange'));};
  document.dispatchEvent(new Event('visibilitychange'));
}"""


def test_a_backgrounded_terminal_resumes_its_own_stream(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({"width": 390, "height": 844})
    page.add_init_script(SOCKET_TRACKER)
    f = attach(page, t)
    f.locator("#agent-terminal").click()
    page.keyboard.type("printf 'BACKGROUND-ANCHOR-%s\\n' 5")
    page.keyboard.press("Enter")
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("BACKGROUND-ANCHOR-5")
    assert _open_sockets(f) == 1

    # The phone puts the tab away and brings it back: the same stream is kept.
    f.locator("body").evaluate(FAKE_BACKGROUND)
    assert f.locator("body").evaluate("()=>document.visibilityState") == "hidden"
    assert f.locator("body").evaluate("()=>window.terminalSockets.length") == 1
    page.wait_for_timeout(500)
    assert _open_sockets(f) == 1, "a hidden tab dropped its terminal stream"
    f.locator("body").evaluate("()=>window.__lecSetForeground(true)")
    assert f.locator("body").evaluate("()=>document.visibilityState") == "visible"
    expect(f.locator("#connection")).to_have_text("Connected")
    assert f.locator("body").evaluate("()=>window.terminalSockets.length") == 1, \
        "resuming the tab reconnected a healthy stream"
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("BACKGROUND-ANCHOR-5")
    f.locator("#agent-terminal").click()
    page.keyboard.type("printf 'AFTER-BACKGROUND-%s\\n' 6")
    page.keyboard.press("Enter")
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("AFTER-BACKGROUND-6")
    assert "AFTER-BACKGROUND-6" in capture(t)


def test_desktop_tab_bar_stays_a_single_row(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({"width": 1280, "height": 900})
    attach(page, t)
    tab = page.locator(".terminal-tab", has_text="Real terminal").bounding_box()
    new = page.get_by_role("button", name="New terminal").bounding_box()
    popout = page.get_by_role("link", name="Pop out ↗").bounding_box()
    assert tab and new and popout
    # The names and the controls share the one row at desk width.
    assert tab["y"] + tab["height"] > new["y"], (tab, new)
    assert new["y"] + new["height"] > tab["y"], (tab, new)
    assert popout["y"] < tab["y"] + tab["height"]


def test_the_tools_button_stays_above_the_keyboard(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({"width": 390, "height": 844})
    f = attach(page, t)
    f.locator("#agent-terminal").click()
    visible = 420
    page.evaluate(FAKE_KEYBOARD, visible)
    expect(f.locator("body")).to_have_class(re.compile("fitted-viewport"), timeout=5000)

    # The Tools control used to be pinned to the layout viewport and hid behind
    # the keys. It has to sit inside the slice a phone can actually see.
    tools = f.locator("#terminal-tools-summary").bounding_box()
    keybar = f.locator("#terminal-keybar").bounding_box()
    assert tools and keybar, (tools, keybar)
    assert tools["y"] + tools["height"] <= visible + 2, (tools, visible)
    assert tools["x"] >= 0 and tools["x"] + tools["width"] <= 390, tools
    assert "Tools" in f.locator("#terminal-tools-summary").inner_text()
    expect(f.locator("#terminal-tools-summary")).to_be_visible()

    # And the menu it opens sits above the keys too, rather than being clipped.
    f.locator("#terminal-tools-summary").click()
    panel = f.locator(".action-menu-panel").bounding_box()
    assert panel and panel["y"] + panel["height"] <= visible + 2, (panel, visible)
    page.screenshot(path="/tmp/lectern-phone-tools-keyboard.png")


def test_an_offline_bootstrap_recovers_when_the_network_returns(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({"width": 390, "height": 844})
    blocked = {"on": True}

    def info(route):
        if blocked["on"]:
            route.abort("internetdisconnected")
        else:
            route.continue_()

    page.route("**/api/term/**/info", info)
    f = attach(page, t)
    # The one bootstrap fetch failed, so there is no pane and no engine yet and
    # nothing in the terminal can retry itself.
    expect(f.locator("#connection")).to_have_text("Connecting", timeout=5000)
    assert f.locator("#agent-pane").count() == 0

    # The network comes back: the bootstrap retries and the view connects,
    # without the operator reloading the page.
    blocked["on"] = False
    f.locator("body").evaluate("()=>window.dispatchEvent(new Event('online'))")
    expect(f.locator("#connection")).to_have_text("Connected", timeout=15000)
    f.locator("#agent-terminal").click()
    page.keyboard.type("printf 'AFTER-OFFLINE-BOOT-%s\\n' 4")
    page.keyboard.press("Enter")
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text(
        "AFTER-OFFLINE-BOOT-4"
    )


def test_a_background_resume_after_a_dropped_stream_keeps_output(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({"width": 390, "height": 844})
    page.add_init_script(SOCKET_TRACKER)
    f = attach(page, t)
    f.locator("#agent-terminal").click()
    page.keyboard.type("printf 'BACKGROUND-KEEP-%s\\n' 9")
    page.keyboard.press("Enter")
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("BACKGROUND-KEEP-9")
    assert _open_sockets(f) == 1

    # The phone puts the tab away and the stream dies while it is gone; coming
    # back reattaches to the same session and keeps what it printed.
    f.locator("body").evaluate(FAKE_BACKGROUND)
    kill_ttyd(t)
    page.wait_for_timeout(500)
    f.locator("body").evaluate("()=>window.__lecSetForeground(true)")
    expect(f.locator("#connection")).to_have_text("Connected", timeout=15000)
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("BACKGROUND-KEEP-9")
    assert _open_sockets(f) == 1, "the background resume left more than one stream"

    f.locator("#agent-terminal").click()
    page.keyboard.type("printf 'AFTER-BACKGROUND-%s\\n' 8")
    page.keyboard.press("Enter")
    expect(f.locator("#agent-terminal .xterm-screen")).to_contain_text("AFTER-BACKGROUND-8")
    out = capture(t)
    assert out.count("AFTER-BACKGROUND-8") == 1, out


@pytest.mark.parametrize('width,height', [(320,640), (390,844), (844,390)])
def test_terminal_close_menu_stays_inside_visible_phone_viewport(page,real_terminal,width,height):
    t=real_terminal
    page.set_viewport_size({'width':width,'height':height})
    frame=attach(page,t)
    expect(frame.locator('#connection')).to_have_text('Connected',timeout=15000)
    expect(frame.locator('.terminal-controls-hint')).not_to_be_visible()
    frame.locator('#terminal-tools-summary').click()
    expect(frame.locator('.terminal-controls-help')).not_to_be_visible()
    frame.locator('#terminal-tools-summary').click()
    summary=page.locator('.terminal-actions > summary')
    def check():
        result=page.locator('.terminal-actions-panel').filter(has=page.locator('.terminal-menu-close')).evaluate('''panel=>{
          const r=panel.getBoundingClientRect(),v=visualViewport;
          return {left:r.left,right:r.right,top:r.top,bottom:r.bottom,
            minX:v.offsetLeft,minY:v.offsetTop,maxX:v.offsetLeft+v.width,maxY:v.offsetTop+v.height,
            hit:panel.contains(document.elementFromPoint(r.left+r.width/2,r.top+r.height/2))};
        }''')
        assert result['left']>=result['minX'] and result['right']<=result['maxX'],result
        assert result['top']>=result['minY'] and result['bottom']<=result['maxY'],result
        assert result['hit'],result
    summary.click();check()
    page.evaluate(FAKE_KEYBOARD,150)
    check()
    page.evaluate(RESTORE_VIEWPORT)
    check()
    page.get_by_role('menuitem',name='Close this view',exact=True).click()
    expect(page.locator('.terminal-empty')).to_be_visible()
    subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)
