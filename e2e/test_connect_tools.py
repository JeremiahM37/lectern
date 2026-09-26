"""The Settings "Connect your AI tools" card, against the mocked settings
fixture (frontend/src/settings/harness.tsx) — same pattern as
test_react_settings.py: a standalone Vite dev server, no real lectern binary,
so a real `claude`/`codex mcp add` is never at risk of running for real.
"""
import os, signal
from pathlib import Path
import subprocess, time, urllib.request
from conftest import PHONE, DESKTOP, _unused_port

R = Path(__file__).parents[1]


def _serve():
    # A fixed port collides with the many other worktrees' dev servers that
    # run concurrently on this shared box — pick a genuinely free one, same
    # as conftest.py does for the real server fixtures. --strictPort makes
    # Vite fail instead of silently binding elsewhere if it loses the race.
    port = _unused_port()
    # `npm exec` spawns vite as a child; terminating just the npm process
    # leaves that child (the thing actually holding the port) running. A new
    # session makes this whole tree its own process group, so _stop below can
    # kill all of it at once instead of leaking a dev server per test run.
    p = subprocess.Popen(
        ["npm", "exec", "vite", "--", "--host", "127.0.0.1", "--port", str(port), "--strictPort"],
        cwd=R / "frontend", stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    url = f"http://127.0.0.1:{port}/react/settings-harness.html"
    for _ in range(100):
        try:
            urllib.request.urlopen(url)
            break
        except Exception:
            time.sleep(.1)
    return p, url


def _stop(p):
    try:
        os.killpg(os.getpgid(p.pid), signal.SIGTERM)
    except ProcessLookupError:
        pass
    p.wait()


def test_connect_tools_card_renders_and_connects(browser):
    p, url = _serve()
    try:
        with browser.new_context() as context:
            x = context.new_page()
            x.goto(url)
            card = x.locator("#connect-tools-heading")
            card.wait_for()
            # It renders above the settings tabs (near the top of Settings).
            tabs = x.locator('.settings-page nav[role="tablist"]')
            assert card.bounding_box()["y"] < tabs.bounding_box()["y"]

            claude_card = x.locator('[data-client="claude-code"]')
            claude_card.get_by_text("Not connected yet").wait_for()
            codex_card = x.locator('[data-client="codex"]')
            codex_card.get_by_text("connected", exact=False).wait_for()

            # Connect click shows success: the client's own output, prefixed
            # with a checkmark, right on the card.
            claude_card.get_by_role("button", name="Connect Claude Code (CLI)").click()
            claude_card.get_by_text("lectern added to /root/.claude.json", exact=False).wait_for()
            claude_card.locator(".connect-result.ok").wait_for()
            x.wait_for_function(
                "calls.some(c => c[0] === '/mcp-clients/claude-code/install' && c[1]?.method === 'POST')"
            )

            # claude.ai / ChatGPT gets an honest external link, never a fake
            # one-click connect button.
            web_card = x.locator('[data-client="web-connectors"]')
            web_link = web_card.get_by_role("link", name="Open claude.ai connectors")
            assert web_link.get_attribute("href") == "https://claude.ai/customize/connectors"

            # Deep links carry the right, real-schema config.
            cursor_href = x.locator('[data-client="cursor"]').get_by_role("link", name="Connect Cursor").get_attribute("href")
            assert cursor_href.startswith("cursor://anysphere.cursor-deeplink/mcp/install?name=lectern&config=")
            decoded = x.evaluate("href => atob(new URL(href).searchParams.get('config'))", cursor_href)
            assert '"command":"/opt/lectern/lectern"' in decoded.replace(" ", "")

            vscode_href = x.locator('[data-client="vscode"]').get_by_role("link", name="Connect VS Code").get_attribute("href")
            assert vscode_href.startswith("vscode:mcp/install?")
            vs_decoded = x.evaluate("href => decodeURIComponent(href.slice('vscode:mcp/install?'.length))", vscode_href)
            assert '"name":"lectern"' in vs_decoded.replace(" ", "")

            # The remote toggle switches the copyable command to the
            # LECTERN_API flavor, filled with this page's own origin.
            origin = x.evaluate("location.origin")
            claude_snippet = claude_card.locator(".connect-snippet pre")
            local_text = claude_snippet.inner_text()
            assert local_text == "claude mcp add --scope user lectern -- /opt/lectern/lectern mcp"
            x.get_by_text("This computer is not the Lectern host").click()
            x.wait_for_function(
                "text => document.querySelector('[data-client=\"claude-code\"] .connect-snippet pre')?.textContent === text",
                arg=f"claude mcp add --scope user lectern -e LECTERN_API={origin} -- lectern mcp",
            )
    finally:
        _stop(p)


def test_connect_tools_no_horizontal_overflow_at_phone_width(browser):
    p, url = _serve()
    try:
        with browser.new_context(viewport=PHONE) as context:
            x = context.new_page()
            x.goto(url)
            x.locator("#connect-tools-heading").wait_for()
            x.locator('[data-client="web-connectors"]').wait_for()
            overflow = x.evaluate("document.documentElement.scrollWidth - document.documentElement.clientWidth")
            assert overflow <= 1, f"horizontal overflow at 390px: {overflow}px"
    finally:
        _stop(p)


def test_connect_tools_renders_at_desktop_width(browser):
    p, url = _serve()
    try:
        with browser.new_context(viewport=DESKTOP) as context:
            x = context.new_page()
            x.goto(url)
            x.locator("#connect-tools-heading").wait_for()
            x.locator('[data-client="claude-code"]').wait_for()
            x.locator('[data-client="web-connectors"]').wait_for()
    finally:
        _stop(p)
