"""End-to-end test for session rename via conversation header and card action."""
from playwright.sync_api import expect

from session_sheet import open_advanced


def test_rename_session_from_conversation_header(page, server):
    """Test renaming a session from the conversation header."""
    # Navigate to sessions page
    page.goto(f"{server}/#sessions")
    page.get_by_text("Live sessions").wait_for()
    
    # Create a new session through the UI
    page.locator("#sess-new").click()
    open_advanced(page)
    page.get_by_label("Session name", exact=True).fill("Header Rename Test")
    page.locator("#ns-go").click()
    
    # Wait for the session to be created
    card = page.locator(".scard", has_text="Header Rename Test")
    card.wait_for()
    session_id = int(card.get_attribute("data-session-id"))
    
    # Open conversation
    card.get_by_text("Chat").click()
    
    # Wait for conversation to open
    page.get_by_text("Live output").wait_for()
    
    # Click the rename button (✎) in the header
    page.locator(".conversation-head .rename-btn").click()
    
    # The title should become an input field
    input_field = page.locator(".conversation-title-edit input")
    input_field.wait_for()
    
    # Clear and type new name
    input_field.fill("Renamed From Header")
    
    # Wait for the server to answer the rename, not just for it to be sent
    with page.expect_response(lambda r: r.request.method == "PATCH" and f"/api/sessions/{session_id}" in r.url and r.ok):
        # Press Enter to save
        input_field.press("Enter")
    
    # Verify via API
    updated = page.request.get(f"{server}/api/sessions/{session_id}").json()
    assert updated["name"] == "Renamed From Header"


def test_rename_escape_cancels_inline_edit(page, server):
    """Test that Escape cancels inline editing without saving."""
    # Navigate to sessions page
    page.goto(f"{server}/#sessions")
    page.get_by_text("Live sessions").wait_for()
    
    # Create a new session through the UI
    page.locator("#sess-new").click()
    open_advanced(page)
    page.get_by_label("Session name", exact=True).fill("Escape Test")
    page.locator("#ns-go").click()
    
    # Wait for the session to be created
    card = page.locator(".scard", has_text="Escape Test")
    card.wait_for()
    session_id = int(card.get_attribute("data-session-id"))
    
    # Open conversation
    card.get_by_text("Chat").click()
    
    # Wait for conversation to open
    page.get_by_text("Live output").wait_for()
    
    # Click the rename button
    page.locator(".conversation-head .rename-btn").click()
    
    # The input should appear
    input_field = page.locator(".conversation-title-edit input")
    input_field.wait_for()
    
    # Type something that shouldn't be saved
    input_field.fill("This Should Not Be Saved")
    
    # Press Escape to cancel
    input_field.press("Escape")
    
    # Wait for the input to disappear and original title to remain
    expect(page.locator("#conversation-title")).to_contain_text("Escape Test")
    
    # Verify via API that nothing changed
    check = page.request.get(f"{server}/api/sessions/{session_id}").json()
    assert check["name"] == "Escape Test"


def test_rename_session_from_card_action_menu(page, server):
    """Test renaming a session from the card action menu."""
    # Navigate to sessions page
    page.goto(f"{server}/#sessions")
    page.get_by_text("Live sessions").wait_for()
    
    # Create a new session through the UI
    page.locator("#sess-new").click()
    open_advanced(page)
    page.get_by_label("Session name", exact=True).fill("Original Name")
    page.locator("#ns-go").click()
    
    # Wait for the session to be created
    card = page.locator(".scard", has_text="Original Name")
    card.wait_for()
    session_id = int(card.get_attribute("data-session-id"))
    # The title turns into a text box while renaming, so hold on to the card by id.
    card = page.locator(f'.scard[data-session-id="{session_id}"]')
    
    # Wait for the API call to complete
    with page.expect_response(lambda r: r.request.method == "PATCH" and f"/api/sessions/{session_id}" in r.url and r.ok):
        # The card's title renames in place: click it, type, Enter.
        card.locator('button.nm').click();card.locator('.nm-edit').fill("New Name From Card");card.locator('.nm-edit').press('Enter')
    
    # Verify the name changed in the UI by finding the card with the new name
    new_card = page.locator(".scard", has_text="New Name From Card")
    expect(new_card).to_be_visible()
    
    # Verify via API
    updated = page.request.get(f"{server}/api/sessions/{session_id}").json()
    assert updated["name"] == "New Name From Card"


def test_rename_session_from_the_visible_card_button(page, server):
    """The ✎ next to a card's name renames it without opening ⋯."""
    page.goto(f"{server}/#sessions")
    page.get_by_text("Live sessions").wait_for()
    page.locator("#sess-new").click()
    page.get_by_label("Session name", exact=True).fill("Pencil Rename")
    page.locator("#ns-go").click()
    card = page.locator(".scard", has_text="Pencil Rename")
    card.wait_for()
    session_id = int(card.get_attribute("data-session-id"))
    card = page.locator(f'.scard[data-session-id="{session_id}"]')
    button = card.get_by_role("button", name="Rename session", exact=True)
    expect(button).to_be_visible()
    button.click()
    field = card.locator("input.nm-edit")
    expect(field).to_be_focused()
    field.fill("Pencil Renamed")
    with page.expect_response(lambda r: r.request.method == "PATCH" and r.url.endswith(f"/api/sessions/{session_id}") and r.ok):
        field.press("Enter")
    assert page.request.get(f"{server}/api/sessions/{session_id}").json()["name"] == "Pencil Renamed"
    expect(card.locator(".nm")).to_have_text("Pencil Renamed")


def test_move_session_to_project_from_the_card_menu(page, server):
    """⋯ → Move to project picks a project by name, and No project takes it out."""
    projects = page.request.get(f"{server}/api/projects").json()
    target = projects[-1]
    page.goto(f"{server}/#sessions")
    page.get_by_text("Live sessions").wait_for()
    page.locator("#sess-new").click()
    page.locator("#ns-project").select_option("")
    page.get_by_label("Session name", exact=True).fill("Project Mover")
    page.locator("#ns-go").click()
    card = page.locator(".scard", has_text="Project Mover")
    card.wait_for()
    session_id = int(card.get_attribute("data-session-id"))
    card = page.locator(f'.scard[data-session-id="{session_id}"]')
    card.locator(".action-menu > summary").first.click()
    card.get_by_role("button", name="Move to project", exact=True).click()
    sheet = page.get_by_role("dialog", name="Move to project", exact=True)
    expect(sheet).to_be_visible()
    sheet.locator("#sp-project").select_option(str(target["id"]))
    with page.expect_response(lambda r: r.request.method == "PATCH" and r.url.endswith(f"/api/sessions/{session_id}") and r.ok):
        sheet.locator("#sp-save").click()
    expect(sheet).not_to_be_visible()
    assert page.request.get(f"{server}/api/sessions/{session_id}").json()["project_id"] == target["id"]

    card.locator(".action-menu > summary").first.click()
    card.get_by_role("button", name="Move to project", exact=True).click()
    sheet = page.get_by_role("dialog", name="Move to project", exact=True)
    expect(sheet.locator("#sp-project")).to_have_value(str(target["id"]))
    sheet.locator("#sp-project").select_option("")
    with page.expect_response(lambda r: r.request.method == "PATCH" and r.url.endswith(f"/api/sessions/{session_id}") and r.ok):
        sheet.locator("#sp-save").click()
    assert page.request.get(f"{server}/api/sessions/{session_id}").json()["project_id"] is None
