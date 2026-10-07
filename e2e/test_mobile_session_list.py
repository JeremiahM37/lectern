"""The phone Sessions page reads as a list: several sessions on one screen,
the title never cut short, and a tap on a card opens its terminal."""
from playwright.sync_api import expect

PHONE_412 = {"width": 412, "height": 839}


def _sessions(page, server, names):
    for name in names:
        page.request.post(
            server + "/api/sessions", data={"name": name, "scratch": True, "agent": "claude"}
        )


def _fully_visible_cards(page):
    return page.evaluate(
        """() => {
          const bar = document.querySelector('#tabbar');
          const bottom = bar && getComputedStyle(bar).display !== 'none'
            ? bar.getBoundingClientRect().top : innerHeight;
          return [...document.querySelectorAll('#sesslist .scard')].filter(card => {
            const r = card.getBoundingClientRect();
            return r.height > 0 && r.top >= 0 && r.bottom <= bottom + 1;
          }).length;
        }"""
    )


def _title_whole(page):
    title = page.locator(".sesshead h2")
    expect(title).to_have_text("Sessions")
    fit = title.evaluate(
        """el => {
          const r = el.getBoundingClientRect(), range = document.createRange();
          range.selectNodeContents(el);
          const text = range.getBoundingClientRect();
          const next = [...el.closest('.sesshead').querySelectorAll(':scope > button, :scope > details')]
            .filter(b => b.getBoundingClientRect().width > 0)
            .map(b => b.getBoundingClientRect());
          const beside = next.filter(b => Math.abs(b.top - r.top) < r.height);
          return {clipped: el.scrollWidth > el.clientWidth + 1 || getComputedStyle(el).textOverflow === 'ellipsis' && el.scrollWidth > el.clientWidth,
                  textRight: text.right, right: r.right,
                  overlaps: beside.some(b => b.left < text.right - 1)};
        }"""
    )
    assert not fit["clipped"], fit
    assert fit["textRight"] <= fit["right"] + 1, fit
    assert not fit["overlaps"], fit


def test_five_sessions_fit_on_a_phone_screen(page, server):
    _sessions(page, server, ["List row %d" % i for i in range(7)])
    page.set_viewport_size(PHONE_412)
    page.goto(server + "/#sessions")
    expect(page.locator("#sesslist .scard")).to_have_count(7, timeout=30000)
    expect(page.locator("#sesslist .scard .spane").first).not_to_be_empty(timeout=15000)
    assert _fully_visible_cards(page) >= 5
    card = page.locator("#sesslist .scard").first
    # Three short lines: name, the newest line of the pane, then agent and actions.
    assert card.bounding_box()["height"] <= 130, card.bounding_box()
    spane = card.locator(".spane")
    assert spane.evaluate("el => el.getBoundingClientRect().height") <= 20
    _title_whole(page)
    assert page.evaluate("document.documentElement.scrollWidth<=innerWidth")


def test_an_open_terminal_never_cuts_the_title_short(page, server):
    _sessions(page, server, ["Kept terminal"])
    for width in (320, 412):
        page.set_viewport_size({"width": width, "height": 839})
        page.goto(server + "/#sessions")
        card = page.locator(".scard", has_text="Kept terminal")
        expect(card).to_be_visible(timeout=20000)
        if not page.locator("#sess-terminals").count():
            card.get_by_role("button", name="⌨ Terminal", exact=True).click()
            expect(page.locator("#terminal-workspace")).to_be_visible()
            page.evaluate("location.hash = '#sessions'")
        back = page.locator("#sess-terminals")
        expect(back).to_be_visible()
        expect(back).to_have_attribute("aria-label", "⌨ Terminals (1)")
        assert back.bounding_box()["height"] >= 40
        _title_whole(page)
        assert page.evaluate("document.documentElement.scrollWidth<=innerWidth")


def test_tapping_a_phone_card_opens_its_terminal(page, server):
    _sessions(page, server, ["Tap me"])
    page.set_viewport_size(PHONE_412)
    page.goto(server + "/#sessions")
    card = page.locator(".scard", has_text="Tap me")
    expect(card.locator(".spane")).not_to_be_empty(timeout=20000)
    card.locator(".spane").click()
    expect(page.locator("#terminal-workspace")).to_be_visible()


def test_the_phone_more_sheet_is_grouped_and_aligned(page, server):
    _sessions(page, server, ["Sheet one", "Sheet two"])
    page.set_viewport_size(PHONE_412)
    page.goto(server + "/#sessions")
    expect(page.locator(".scard", has_text="Sheet one")).to_be_visible(timeout=20000)
    page.locator("#sess-more > summary").click()
    sheet = page.locator("#sess-more .sess-sheet")
    expect(sheet).to_be_visible()
    expect(sheet.locator(".sess-sheet-group")).to_have_count(3)
    items = sheet.locator(".sess-sheet-item")
    expect(items).to_have_count(4)
    lefts = set()
    for i in range(items.count()):
        icon = items.nth(i).locator(".sheet-ic").bounding_box()
        label = items.nth(i).locator("span").bounding_box()
        lefts.add((round(icon["x"]), round(label["x"])))
        assert items.nth(i).bounding_box()["height"] >= 44
    assert len(lefts) == 1, lefts
