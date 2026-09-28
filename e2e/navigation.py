"""Use the desktop sidebar or compact navigation as a person would."""
from playwright.sync_api import expect


def navigate(page, view):
    primary = page.locator(f'.tab[data-tab="{view}"]')
    if primary.count():
        primary.click()
        return
    page.locator('#nav-overflow > summary').click()
    entry = page.locator(f'#nav-overflow [data-nav-target="{view}"]')
    expect(entry).to_be_visible()
    entry.click()
    expect(page.locator('#nav-overflow')).not_to_have_attribute('open', '')


def card_actions(card):
    summary = card.locator(':scope > .btnrow .action-menu > summary')
    # Cards have a single top-level action menu; nested Memory is separate.
    if not summary.count():
        summary = card.locator('.action-menu > summary').first
    if not summary.locator('..').get_attribute('open') == '':
        summary.click()


def session_filters(page):
    details = page.locator('.session-filter-options')
    if details.get_attribute('open') is None:
        details.locator('summary').click()


def restore_tools(page):
    button = page.locator('#sess-recent')
    if button.get_attribute('aria-expanded') != 'true':
        button.click()
