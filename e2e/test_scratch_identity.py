"""Blank shells must be distinguishable: the scratch folder names the card.

Two quick shells on one machine used to be two identical "Shell · <target>"
cards, so there was no way to tell which one held what. The card title is now
the scratch folder the target made for it — unique and stable for the shell's
life — the full path stays readable and copyable, and a manual rename still
wins and persists across a reload. Nothing here renames stored rows.
"""
from pathlib import PurePosixPath

from playwright.sync_api import expect


def _shells(page, server, count):
    return [
        page.request.post(server + "/api/shells", data={}).json()
        for _ in range(count)
    ]


def _folder(shell):
    return PurePosixPath(shell["workdir"]).name


def test_scratch_cards_are_named_by_folder_and_keep_a_manual_rename(page, server):
    first, second = _shells(page, server, 2)
    first_folder, second_folder = _folder(first), _folder(second)
    assert first["workdir"] != second["workdir"]
    assert first_folder != second_folder

    page.set_viewport_size({"width": 320, "height": 844})
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    page.goto(server + "/#sessions")
    scratch = page.locator("#scratch-terminals")
    first_card = scratch.locator(f'.scard[data-session-id="{first["id"]}"]')
    second_card = scratch.locator(f'.scard[data-session-id="{second["id"]}"]')
    expect(first_card).to_be_visible(timeout=20000)
    expect(second_card).to_be_visible(timeout=20000)

    # Distinct titles: the folder, not the shared generic name.
    expect(first_card.locator(".nm")).to_have_text(first_folder)
    expect(second_card.locator(".nm")).to_have_text(second_folder)
    # The machine the shell runs on stays readable next to the folder title.
    expect(first_card.locator(".scard-project")).to_contain_text("Shell ·")

    # The full path is on the card, wraps instead of overflowing, and copies.
    page.context.grant_permissions(["clipboard-read", "clipboard-write"])
    for card, shell in ((first_card, first), (second_card, second)):
        code = card.locator(".scard-path code")
        expect(code).to_have_text(shell["workdir"])
        expect(code).to_have_attribute("title", shell["workdir"])
        assert card.locator(".scard-path").evaluate(
            "el=>el.scrollWidth<=el.clientWidth+1"
        )
    first_card.get_by_role("button", name="Copy path").click()
    assert page.evaluate("navigator.clipboard.readText()") == first["workdir"]

    # A phone must not get a sideways scrollbar from the longer titles or paths.
    assert page.evaluate("document.documentElement.scrollWidth<=innerWidth+1")
    for card in (first_card, second_card):
        assert card.locator(".nm").evaluate("el=>el.scrollWidth<=el.clientWidth+1")

    # An explicit rename replaces the folder title and survives a reload.
    page.once("dialog", lambda dialog: dialog.accept("deploy scratch"))
    first_card.get_by_role("button", name="Rename").click()
    expect(first_card.locator(".nm")).to_have_text("deploy scratch", timeout=10000)
    stored = page.request.get(f'{server}/api/sessions/{first["id"]}').json()
    assert stored["name"] == "deploy scratch"

    page.reload()
    first_card = scratch.locator(f'.scard[data-session-id="{first["id"]}"]')
    second_card = scratch.locator(f'.scard[data-session-id="{second["id"]}"]')
    expect(first_card.locator(".nm")).to_have_text("deploy scratch", timeout=20000)
    # The shell nobody renamed keeps its own folder title, not the renamed one.
    expect(second_card.locator(".nm")).to_have_text(second_folder)
    # The rename never hides where the shell actually lives.
    expect(first_card.locator(".scard-path code")).to_have_text(first["workdir"])
    assert page.evaluate("document.documentElement.scrollWidth<=innerWidth+1")
    assert not errors
