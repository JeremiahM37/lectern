"""Real mobile terminal-tab gestures against two live tmux sessions."""
import subprocess

import pytest
from playwright.sync_api import expect

from test_terminal_workspace import real_terminal


def _touch_swipe(page, x1, y1, x2, y2):
    _touch_path(page, [(x1, y1), (x2, y2)])


def _touch_path(page, points):
    cdp = page.context.new_cdp_session(page)
    x1, y1 = points[0]
    cdp.send("Input.dispatchTouchEvent", {"type": "touchStart",
        "touchPoints": [{"x": x1, "y": y1, "id": 1}]})
    for x, y in points[1:]:
        cdp.send("Input.dispatchTouchEvent", {"type": "touchMove",
            "touchPoints": [{"x": x, "y": y, "id": 1}]})
    cdp.send("Input.dispatchTouchEvent", {"type": "touchEnd", "touchPoints": []})


def _session_frame(page, session_id):
    suffix = f"/terminal/session/{session_id}"
    selector = f'iframe[src="{suffix}"], iframe[src^="{suffix}?"]'
    # Selecting a tab precedes navigation of its new iframe. Wait for the
    # terminal's connection rather than snapshotting page.frames too early.
    expect(page.frame_locator(selector).locator("#connection")).to_have_text(
        "Connected", timeout=15000
    )
    return page.locator(selector).element_handle().content_frame()


@pytest.mark.parametrize("width", [320, 390])
def test_mobile_swipe_switches_live_terminal_tabs_without_stealing_scroll_or_selection(
    page, real_terminal, width, tmp_path
):
    t = real_terminal
    second_root = tmp_path / "second-workspace"
    second_root.mkdir()
    subprocess.run(["tmux", "new-session", "-d", "-s", "terminal-two",
                    "-c", str(second_root), "bash --norc"], env=t["env"], check=True)
    second = t["api"]("/sessions/adopt", {
        "target_id": t["target_id"], "tmux_session": "terminal-two",
        "workdir": str(second_root), "name": "Second terminal", "agent": "claude",
    })
    page.set_viewport_size({"width": width, "height": 900})
    page.goto(t["url"] + "/#sessions")

    page.locator(".scard", has_text="Real terminal").get_by_role(
        "button", name="⌨ Attach"
    ).click()
    expect(page.locator(".terminal-tab", has_text="Real terminal")).to_have_attribute(
        "aria-selected", "true", timeout=15000
    )
    # Compact mode folds the primary nav; an explicit sessions route is the
    # same supported escape used by the terminal's Browse action.
    page.goto(t["url"] + "/#sessions")
    page.locator(".scard", has_text="Second terminal").get_by_role(
        "button", name="⌨ Attach"
    ).click()
    expect(page.locator(".terminal-tab", has_text="Second terminal")).to_have_attribute(
        "aria-selected", "true", timeout=15000
    )
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
    _session_frame(page, second["id"])
    panel = page.locator(".terminal-tabpanel:not([hidden]) iframe")
    box = panel.bounding_box()
    assert box and box["width"] > 250 and box["height"] > 300
    page.screenshot(path="/tmp/lectern-mobile-tabs-before.png", full_page=False)

    # A real CDP touch flick anywhere in the active terminal selects the
    # adjacent tab.  The tab identity and its attached iframe remain mounted.
    _touch_swipe(page, box["x"] + box["width"] * .25, box["y"] + box["height"] * .5,
                 box["x"] + box["width"] * .65, box["y"] + box["height"] * .5)
    expect(page.locator(".terminal-tab", has_text="Real terminal")).to_have_attribute(
        "aria-selected", "true", timeout=3000
    )
    page.screenshot(path="/tmp/lectern-mobile-tabs-after-right.png", full_page=False)
    real_frame = _session_frame(page, t["id"])
    real_input = real_frame.locator('textarea[aria-label="Terminal input"]')
    real_input.focus()
    page.keyboard.type("printf 'TAB_REAL_MARKER\\n'", delay=1); page.keyboard.press("Enter")
    expect(real_frame.locator(".xterm-screen")).to_contain_text("TAB_REAL_MARKER", timeout=10000)
    _touch_swipe(page, box["x"] + box["width"] * .65, box["y"] + box["height"] * .5,
                 box["x"] + box["width"] * .25, box["y"] + box["height"] * .5)
    expect(page.locator(".terminal-tab", has_text="Second terminal")).to_have_attribute(
        "aria-selected", "true", timeout=3000
    )
    page.screenshot(path="/tmp/lectern-mobile-tabs-after-left.png", full_page=False)

    frame = _session_frame(page, second["id"])
    state = frame.evaluate("window.__lecTerminalState?.()")
    assert state and state["mouseTrackingMode"] == "none"
    second_input = frame.locator('textarea[aria-label="Terminal input"]')
    second_input.focus()
    page.keyboard.type("printf 'SELECTABLE_MARKER\\n'", delay=1); page.keyboard.press("Enter")
    expect(frame.locator(".xterm-screen")).to_contain_text("SELECTABLE_MARKER", timeout=10000)
    screen = frame.locator(".xterm-screen").bounding_box()
    assert screen
    page.mouse.move(screen["x"] + 8, screen["y"] + 12)
    page.mouse.down()
    page.mouse.move(screen["x"] + min(240, screen["width"] - 8), screen["y"] + 12, steps=8)
    page.mouse.up()
    assert frame.evaluate("window.__lecTerminalState?.().hasSelection")
    active_before = page.locator('.terminal-tab[aria-selected="true"]').inner_text()
    # A horizontal text selection is owned by xterm and must not navigate.
    _touch_swipe(page, box["x"] + box["width"] * .25, box["y"] + box["height"] * .5,
                 box["x"] + box["width"] * .65, box["y"] + box["height"] * .5)
    expect(page.locator('.terminal-tab[aria-selected="true"]')).to_have_text(active_before)

    # The tab strip is the explicit fallback while a full-screen terminal
    # application owns mouse reporting; the body gesture above still yielded.
    strip = page.locator('.terminal-tablist').bounding_box()
    assert strip
    _touch_swipe(page, strip["x"] + strip["width"] * .2, strip["y"] + strip["height"] * .5,
                 strip["x"] + strip["width"] * .8, strip["y"] + strip["height"] * .5)
    expect(page.locator('.terminal-tab[aria-selected="true"]', has_text="Real terminal")).to_be_visible()
    page.locator('.terminal-tab', has_text="Second terminal").click()

    frame.locator(".xterm-screen").click(position={"x": screen["width"] * .8, "y": screen["height"] * .8})
    assert not frame.evaluate("window.__lecTerminalState?.().hasSelection")
    # Vertical reading gestures that briefly backtrack into a diagonal path
    # also stay in the terminal; endpoint-only checks would misclassify this.
    _touch_path(page, [(box["x"] + box["width"] * .25, box["y"] + box["height"] * .7),
                       (box["x"] + box["width"] * .28, box["y"] + box["height"] * .35),
                       (box["x"] + box["width"] * .65, box["y"] + box["height"] * .5)])
    expect(page.locator('.terminal-tab[aria-selected="true"]')).to_have_text(active_before)

    # A terminal application that negotiates mouse tracking still yields a
    # deliberate horizontal flick on its body: a full-screen TUI must not make
    # the next terminal unreachable. Vertical drags keep going to the app.
    second_input.focus()
    page.keyboard.type("printf '\\033[?1000h'", delay=1); page.keyboard.press("Enter")
    for _ in range(50):
        if frame.evaluate("window.__lecTerminalState?.().mouseTrackingMode") != "none": break
        page.wait_for_timeout(100)
    assert frame.evaluate("window.__lecTerminalState?.().mouseTrackingMode") != "none"
    _touch_swipe(page, box["x"] + box["width"] * .25, box["y"] + box["height"] * .5,
                 box["x"] + box["width"] * .65, box["y"] + box["height"] * .5)
    expect(page.locator('.terminal-tab[aria-selected="true"]')).to_have_text(
        "Real terminal", timeout=3000
    )
    page.locator('.terminal-tab', has_text="Second terminal").click()
    expect(page.locator('.terminal-tab[aria-selected="true"]')).to_have_text(active_before)
    second_input.focus()
    page.keyboard.type("printf '\\033[?1000l'", delay=1); page.keyboard.press("Enter")

    # The compact toolbar keeps secondary actions reachable through its menu,
    # and a browser keyboard resize does not clip the active terminal.
    page.locator('.terminal-actions>summary').click()
    expect(page.get_by_role("menuitem", name="Close this view")).to_be_visible()
    page.keyboard.press("Escape")
    page.set_viewport_size({"width": width, "height": 640})
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
    assert page.locator(".terminal-tab[aria-selected=true]").count() == 1
