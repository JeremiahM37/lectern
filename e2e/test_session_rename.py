"""End-to-end test for session rename via conversation header and card action."""
from playwright.sync_api import expect

from session_sheet import open_advanced


def test_rename_session_from_conversation_header(page, server):
    """Test renaming a session from the conversation header."""
    # Navigate to sessions page
    page.goto(f"{server}/#sessions")
    page.get_by_text("Live sessions").wait_for()
    
    # Create a new session through the UI
    page.get_by_text("+ New session").click()
    open_advanced(page)
    page.get_by_label("Name").fill("Header Rename Test")
    page.get_by_text("▶ Start session").click()
    
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
    page.get_by_text("+ New session").click()
    open_advanced(page)
    page.get_by_label("Name").fill("Escape Test")
    page.get_by_text("▶ Start session").click()
    
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
    page.get_by_text("+ New session").click()
    open_advanced(page)
    page.get_by_label("Name").fill("Original Name")
    page.get_by_text("▶ Start session").click()
    
    # Wait for the session to be created
    card = page.locator(".scard", has_text="Original Name")
    card.wait_for()
    session_id = int(card.get_attribute("data-session-id"))
    
    # Set up dialog handler before clicking the rename button
    page.once("dialog", lambda dialog: dialog.accept("New Name From Card"))
    
    # Wait for the API call to complete
    with page.expect_response(lambda r: r.request.method == "PATCH" and f"/api/sessions/{session_id}" in r.url and r.ok):
        # The card's own Rename button - this will trigger the prompt dialog
        card.get_by_role("button", name="✎ Rename").click()
    
    # Verify the name changed in the UI by finding the card with the new name
    new_card = page.locator(".scard", has_text="New Name From Card")
    expect(new_card).to_be_visible()
    
    # Verify via API
    updated = page.request.get(f"{server}/api/sessions/{session_id}").json()
    assert updated["name"] == "New Name From Card"
