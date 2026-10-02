"""The desktop sidebar is the phone's three items plus an in-place More
group; neither it nor the compact More can overlay the terminal."""
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_terminal_tabs import attach, frame, ready


def test_desktop_sidebar_stays_outside_a_connected_terminal(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({"width": 1440, "height": 900})
    page.goto(t["url"] + "/#sessions")
    expect(page.locator("#nav-overflow")).to_have_count(0)
    expect(page.locator("#tabbar > .tab")).to_have_count(3)
    for destination in ["sessions", "approvals", "settings"]:
        expect(page.locator(f'#tabbar > .tab[data-nav-target="{destination}"]')).to_be_visible()
    # Everything else is one click away, in place in the sidebar.
    group = page.locator("#nav-more-group")
    expect(group.locator('[data-nav-target="overview"]')).to_be_hidden()
    group.locator("summary").click()
    for destination in ["tasks", "terminals", "overview", "issues", "media", "evals"]:
        expect(group.locator(f'[data-nav-target="{destination}"]')).to_be_visible()
    # Machines and Plugins are Settings tabs; the sidebar does not repeat them.
    for duplicate in ["machines", "plugins"]:
        expect(page.locator(f'#tabbar [data-nav-target="{duplicate}"]')).to_have_count(0)
    attach(page, "Real terminal")
    terminal = frame(page, t["id"])
    ready(terminal)
    nav = page.locator("#tabbar").bounding_box()
    pane = terminal.owner.bounding_box()
    assert nav["x"] + nav["width"] <= pane["x"], (nav, pane)
    expect(page.locator("#tabbar .action-menu-panel")).to_have_count(0)
    terminal.locator("#agent-terminal").click()
    page.keyboard.type("echo DESKTOP-NAV-INPUT")
    page.keyboard.press("Enter")
    expect(terminal.locator(".xterm-screen")).to_contain_text("DESKTOP-NAV-INPUT")


def test_open_mobile_more_is_removed_when_resizing_to_desktop(page, server):
    page.set_viewport_size({"width": 390, "height": 844})
    page.goto(server + "/#sessions")
    expect(page.locator("#tabbar > .tab")).to_have_count(3)
    page.locator("#nav-overflow > summary").click()
    expect(page.locator('#nav-overflow [data-nav-target="tasks"]')).to_be_visible()
    page.set_viewport_size({"width": 1440, "height": 900})
    expect(page.locator("#nav-overflow")).to_have_count(0)
    page.locator("#nav-more-group > summary").click()
    page.locator('.tab[data-tab="tasks"]').click()
    expect(page.locator("#board")).to_be_visible()
    page.set_viewport_size({"width": 390, "height": 844})
    expect(page.locator("#nav-overflow")).not_to_have_attribute("open", "")
    page.locator("#nav-overflow > summary").click()
    page.locator('#nav-overflow [data-nav-target="terminals"]').click()
    expect(page.locator("#nav-overflow")).not_to_have_attribute("open", "")
