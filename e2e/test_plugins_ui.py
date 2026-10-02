"""Settings → Plugins through the real browser: install from a folder, consent
to exactly what the preview shows, and see the contributions arrive where they
belong — Appearance, the command palette, and the terminal's quick commands."""
from pathlib import Path

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE

EXAMPLE = str(Path(__file__).resolve().parents[1] / "examples" / "plugins" / "hello")


@pytest.mark.parametrize("page", [PHONE, DESKTOP], indirect=True, ids=["phone390", "desktop1440"])
def test_install_consent_and_contributions_visible(page, server):
    # The example theme is a dark palette; the app follows the device's theme.
    page.emulate_media(color_scheme="dark")
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    try:
        page.goto(server + "/#settings/plugins")
        for bundled in ("lectern.agent-catalog", "lectern.spec-kit", "lectern.maestro", "lectern.delegate"):
            expect(page.locator(f'[data-plugin="{bundled}"] .plugin-status')).to_have_text("on")
        expect(page.locator('[data-plugin="example.hello"]')).to_have_count(0)

        page.locator(".plugin-add .segmented label", has_text="Folder on this server").click()
        page.fill("#plugin-path", EXAMPLE)
        page.click("#plugin-preview-path")
        dialog = page.locator("#plugin-consent")
        expect(dialog).to_be_visible()
        expect(dialog.get_by_role("heading", name="Install Hello?")).to_be_visible()
        expect(dialog.locator('[data-cap="host_exec"]')).to_contain_text("Runs commands on the Lectern server")
        expect(dialog.locator('[data-cap="host_exec"]')).to_contain_text("task.finished: python3 hooks/on_finish.py")
        expect(dialog.locator('[data-cap="notify"]')).to_be_visible()
        if page.viewport_size["width"] < 600:
            box = dialog.bounding_box()
            assert box["x"] >= 0 and box["x"] + box["width"] <= 390, box
        # Nothing is installed until the person ticks the box.
        install = page.locator("#plugin-consent-install")
        expect(install).to_be_disabled()
        page.locator("#plugin-consent-check").check()
        expect(install).to_be_enabled()
        install.click()
        expect(dialog).to_have_count(0)
        card = page.locator('[data-plugin="example.hello"]')
        expect(card.locator(".plugin-status")).to_have_text("on")
        expect(card).to_contain_text("Quick commands 1")

        # Details: capabilities and the skill it adds.
        card.get_by_role("button", name="Details").click()
        expect(card.locator(".plugin-detail")).to_contain_text("commit-message")

        # Its theme is offered in Appearance, and applies.
        page.locator('[data-settings="appearance"]').click()
        page.select_option("#appearance-preset", "example.hello/meadow")
        expect(page.locator("html")).to_have_attribute("style", __import__("re").compile("--bg: #0b1410"))
        page.select_option("#appearance-preset", "")

        # Its palette command is in the palette and opens Settings → Plugins.
        page.get_by_role("button", name="Search sessions and actions", exact=True).click()
        page.get_by_role("combobox", name="Search sessions, tasks, and actions").fill("Hello: manage")
        palette = page.get_by_role("dialog", name="Search Lectern")
        option = palette.get_by_role("option", name=__import__("re").compile("Hello: manage plugins"))
        expect(option).to_be_visible()
        option.click()
        expect(page.locator('[data-settings="plugins"]')).to_have_attribute("aria-selected", "true")

        # Its quick command reaches terminals (the key row and Snippets read this list).
        ui = page.request.get(f"{server}/api/plugins/contributions").json()
        assert [row["text"] for row in ui["quick_commands"]] == ["git status --short"], ui

        # Turned off, it contributes nothing.
        card.get_by_role("button", name="Turn off").click()
        expect(card.locator(".plugin-status")).to_have_text("off")
        assert page.request.get(f"{server}/api/plugins/contributions").json()["quick_commands"] == []
        assert not errors, errors
    finally:
        page.request.delete(f"{server}/api/plugins/example.hello")


def test_turning_off_the_agent_catalog_asks_first(page, server):
    page.goto(server + "/#settings/plugins")
    card = page.locator('[data-plugin="lectern.agent-catalog"]')
    expect(card.locator(".plugin-status")).to_have_text("on")
    # Dismiss inside the handler: a confirm() left open blocks the click from
    # returning, which made this test time out on slower CI machines.
    seen = []
    def dismiss(d):
        seen.append(d.message)
        d.dismiss()
    page.once("dialog", dismiss)
    card.get_by_role("button", name="Turn off").click()
    for _ in range(50):
        if seen:
            break
        page.wait_for_timeout(100)
    message = seen[0] if seen else ""
    assert "Add from catalog" in message and "will be empty" in message, message
    # Dismissed: nothing changed.
    expect(card.locator(".plugin-status")).to_have_text("on")
