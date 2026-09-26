"""Device pairing (internal/pairing, docs/remote-access.md): an owner mints a
code from an authenticated browser; a second, wholly unauthenticated browser
context exchanges it for a paired-device credential and can use the app —
until the owner revokes it.

Uses the `pairing_server` fixture (token mode + LECTERN_DEVICE_PAIRING=1), not
the open `server` fixture: pairing only proves anything when the alternative
really is locked out.
"""
import re

from playwright.sync_api import expect

from conftest import PHONE, DESKTOP


def _open_settings_tab(page, name):
    if page.get_by_role("button", name="Show navigation", exact=True).is_visible():
        page.get_by_role("button", name="Show navigation", exact=True).click()
    button = page.locator('.tab[data-tab="targets"]')
    if not button.is_visible():
        page.locator("#nav-overflow > summary").click()
        page.locator('[data-nav-target="targets"]').click()
    else:
        button.click()
    page.get_by_role("tab", name=name).click()


def test_device_pairing_full_flow(browser, pairing_server):
    # Owner: a normal desktop browser, signed in with the admin bootstrap token.
    owner_ctx = browser.new_context(viewport=DESKTOP)
    owner = owner_ctx.new_page()
    try:
        owner.goto(pairing_server)
        owner.evaluate("localStorage.setItem('lec-token','pairsecret123')")
        owner.reload()
        expect(owner.locator(".col-head")).to_have_count(6, timeout=10000)

        _open_settings_tab(owner, "Devices")
        owner.get_by_role("button", name="Pair a phone").click()
        code_el = owner.locator(".pairing-code-text")
        expect(code_el).to_be_visible(timeout=5000)
        code = code_el.inner_text()
        assert code, "expected a pairing code to render"

        # Second context: a phone-width browser with NO credential at all —
        # not the owner's token, no cookie, nothing.
        phone_ctx = browser.new_context(viewport=PHONE)
        phone = phone_ctx.new_page()
        try:
            phone.goto(pairing_server + "/pair")
            # Confirm the exchange endpoint really is the only door: every
            # ordinary API call is refused before pairing.
            pre_status = phone.evaluate("async () => (await fetch('/api/tasks')).status")
            assert pre_status == 401, f"expected 401 before pairing, got {pre_status}"

            phone.fill("#pair-code", code)
            phone.fill("#pair-name", "Test Phone")
            phone.click("#pair-submit")

            # Lands in the real app, authenticated as the paired device.
            expect(phone.locator("#topbar")).to_be_visible(timeout=10000)
            expect(phone.locator("#conn-label")).to_have_text("LIVE", timeout=10000)

            # Performs a real API action as the device: whoami resolves to
            # KindDevice, and an ordinary read succeeds with no token at all.
            whoami = phone.evaluate("async () => (await fetch('/api/whoami')).json()")
            assert whoami["kind"] == "device", whoami
            tasks_status = phone.evaluate("async () => (await fetch('/api/tasks')).status")
            assert tasks_status == 200, f"expected the paired device to read /api/tasks, got {tasks_status}"

            # Owner sees exactly one paired device and revokes it.
            owner.reload()
            _open_settings_tab(owner, "Devices")
            expect(owner.locator(".device-row")).to_have_count(1, timeout=5000)
            owner.once("dialog", lambda d: d.accept())
            owner.get_by_role("button", name="Revoke").click()
            expect(owner.locator(".device-row")).to_have_count(0, timeout=5000)

            # The second context gets bounced back to /pair the next time the
            # app tries to load anything — exactly what reopening it looks like.
            # (expect().to_have_url polls current state, unlike wait_for_url,
            # which only fires on a future navigation event and can miss one
            # that already completed by the time it's called.)
            phone.reload()
            expect(phone).to_have_url(re.compile(r".*/pair$"), timeout=10000)
            expect(phone.locator("#pair-submit")).to_be_visible()
        finally:
            phone_ctx.close()
    finally:
        owner_ctx.close()
