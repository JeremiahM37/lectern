"""`lectern up` opens the browser on /#sessions/new: the page lands on
Sessions with the Start an agent sheet already open."""
import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE
from test_light_mode_sweep import Sweep, light


@pytest.mark.parametrize("theme", ["light", "dark"])
@pytest.mark.parametrize("page", [DESKTOP, PHONE], indirect=True, ids=["desk", "phone"])
def test_up_lands_on_start_an_agent(page, server, theme):
    light(page, theme)
    page.goto(server + "/#sessions/new")
    dialog = page.get_by_role("dialog", name="Start an agent", exact=True)
    expect(dialog).to_be_visible(timeout=10000)
    # The link is used once: a reload does not reopen the dialog.
    expect(page).to_have_url(server + "/#sessions")
    label = "phone" if page.viewport_size["width"] < 600 else "desk"
    sweep = Sweep(page, label + "-up-landing", theme)
    sweep.check("new-session")
    sweep.done()
