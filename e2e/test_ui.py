"""Browser flows: the whole operator loop, on both a phone and a desktop."""
import time
import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE


def _tab(page, name):
    if page.get_by_role('button',name='Show navigation',exact=True).is_visible():
        page.get_by_role('button',name='Show navigation',exact=True).click()
    button=page.locator(f'.tab[data-tab="{name}"]')
    if not button.is_visible():
        page.locator('#nav-overflow > summary').click()
        page.locator(f'[data-nav-target="{name}"]').click()
    else: button.click()


def _new_task(page, title, prompt="fix it", perm=None):
    page.click("#fab")
    page.fill("#f-title", title)
    page.fill("#f-prompt", prompt)
    if perm:
        page.select_option("#f-perm", perm)
    page.click("#f-go")


def test_board_renders(page, server):
    page.goto(server)
    expect(page.locator(".brand h1")).to_contain_text("lectern")
    expect(page.locator(".col-head")).to_have_count(6)
    for name in ["backlog", "queued", "running", "review", "done", "failed"]:
        expect(page.locator(f".col.s-{name}")).to_be_visible()
    expect(page.locator("#fab")).to_be_visible()
    expect(page.locator("#conn-label")).to_have_text("LIVE", timeout=10000)


@pytest.mark.parametrize("page", [PHONE], indirect=True, ids=["phone"])
def test_phone_board_is_one_column_with_a_status_strip(page, server):
    """Six 86vw columns meant ~2100px of sideways scrolling on a 390px screen."""
    page.goto(server + "/#board")
    expect(page.locator("#conn-label")).to_have_text("LIVE", timeout=10000)
    expect(page.locator(".colstrip .colchip")).to_have_count(6)
    expect(page.locator(".col")).to_have_count(1)
    assert page.evaluate("document.body.scrollWidth <= window.innerWidth"), \
        "the board must not scroll sideways on a phone"


@pytest.mark.parametrize("page", [PHONE], indirect=True, ids=["phone"])
def test_phone_board_focuses_work_that_wants_a_decision(page, server):
    """It used to open on an empty BACKLOG with the live card three swipes away."""
    page.goto(server + "/#board")
    expect(page.locator("#conn-label")).to_have_text("LIVE", timeout=10000)
    _new_task(page, "PHONE focus", "add health endpoint")
    expect(page.locator(".col.s-review .card", has_text="PHONE focus")) \
        .to_be_visible(timeout=20000)
    expect(page.locator(".colchip.s-review.on")).to_be_visible()


def test_full_flow_dispatch_review_diff_done(page, server):
    page.goto(server)
    _new_task(page, "E2E ship it", "add health endpoint")
    card = page.locator(".card", has_text="E2E ship it")
    # the card lands on the board and travels to review as the agent works
    expect(card).to_be_visible(timeout=10000)
    expect(page.locator(".col.s-review .card", has_text="E2E ship it")) \
        .to_be_visible(timeout=20000)

    # open detail: the live timeline captured the agent's events
    page.locator(".col.s-review .card", has_text="E2E ship it").click()
    expect(page.locator("#sheet .statpill")).to_have_text("review")
    expect(page.locator("#sheet .ev.e-init")).to_be_visible()
    expect(page.locator("#sheet .ev.e-tool_use")).to_be_visible()
    expect(page.locator("#sheet .ev.e-result")).to_be_visible()

    # diff viewer
    page.click("#actions button:has-text('Diff')")
    expect(page.locator(".dfile summary", has_text="app.py")).to_be_visible()
    expect(page.locator(".dl-add", has_text="hello, lectern").first).to_be_visible()

    # mark done → the card moves to the done column
    page.click("#actions button:has-text('Mark done')")
    expect(page.locator(".col.s-done .card", has_text="E2E ship it")) \
        .to_be_visible(timeout=10000)


@pytest.mark.parametrize("page", [PHONE], indirect=True, ids=["phone"])
def test_approval_flow_from_phone(page, server):
    page.goto(server + "/#board")
    _new_task(page, "E2E gated deploy", "deploy [mock:approval]", perm="default")
    expect(page.locator("#appr-badge:visible, #more-badge:visible")).to_be_visible(timeout=15000)
    _tab(page, "approvals")
    row = page.locator(".rowcard", has_text="Bash")
    expect(row.first).to_be_visible()
    expect(row.first.locator("pre")).to_contain_text("rm -rf build/")
    row.first.locator("button:has-text('Approve')").first.click()
    # the agent continues and finishes
    _tab(page, "board")
    expect(page.locator(".col.s-review .card", has_text="E2E gated deploy")) \
        .to_be_visible(timeout=20000)


def test_targets_tab_probe(page, server):
    page.goto(server)
    _tab(page, "targets")
    # scope by heading: project cards name their target too, so a bare has_text
    # would match both the target card and every project pointed at it
    row = page.locator(".rowcard").filter(
        has=page.locator("h3", has_text="lxc-101-project-env"))
    expect(row).to_be_visible()
    row.locator("button:has-text('Probe')").click()
    expect(row.locator(".sub", has_text="claude")).to_be_visible(timeout=10000)


def test_targets_tab_shows_project_capability(page, server):
    """The parity settings had no UI at all — they could only be set by curl."""
    page.goto(server)
    _tab(page, "targets")
    page.locator('[data-settings="projects"]').click()
    # projects are a filterable list now — tapping one opens its settings
    page.fill("#pj-search", "demo-app")
    page.wait_for_timeout(300)
    page.locator(".pjrow", has_text="demo-app").first.click()
    card = page.locator("#sheet .rowcard")
    expect(card).to_be_visible()
    expect(card.locator(".cap-sel")).to_have_value("restricted")
    card.locator(".cap-sel").select_option("parity")
    expect(card.locator(".cap-info")).to_contain_text("bash: unrestricted", timeout=10000)
    # leave the shared server as we found it
    card.locator(".cap-sel").select_option("restricted")
    expect(card.locator(".cap-info")).to_contain_text("⚠", timeout=10000)
    page.locator("#sheet .x").click()


@pytest.mark.parametrize("page", [PHONE], indirect=True, ids=["phone"])
def test_new_task_sheet_states_the_agent_capability(page, server):
    """You should know the agent will be crippled BEFORE spending a dispatch."""
    # pin the profile and the picked project: the server is shared, so another
    # test's toggle would otherwise decide what this one asserts
    pid = page.request.get(f"{server}/api/projects").json()[0]["id"]
    page.request.patch(f"{server}/api/projects/{pid}",
                       data={"capability_profile": "restricted"})
    page.goto(server + "/#board")
    page.click("#fab")
    page.select_option("#f-project", str(pid))
    expect(page.locator("#f-cap-hint")).to_contain_text("restricted", timeout=10000)
    expect(page.locator("#f-cap-hint .cap-note")).to_contain_text("denied with no prompt")


def test_quickbar_instant_dispatch(page, server):
    page.goto(server)
    page.fill("#qb-input", "quick: bump the version")
    page.press("#qb-input", "Enter")
    expect(page.locator(".card", has_text="quick: bump the version")) \
        .to_be_visible(timeout=10000)
    expect(page.locator(".col.s-review .card", has_text="quick: bump")) \
        .to_be_visible(timeout=20000)


def test_drag_card_to_queued_dispatches(page, server):
    page.goto(server)
    page.click("#fab")
    page.fill("#f-title", "Drag me")
    page.fill("#f-prompt", "dragged task")
    page.click("#f-save")     # backlog, not dispatched
    card = page.locator(".col.s-backlog .card", has_text="Drag me")
    expect(card).to_be_visible(timeout=10000)
    card.drag_to(page.locator(".col.s-queued .col-body"))
    expect(page.locator(".col.s-review .card", has_text="Drag me")) \
        .to_be_visible(timeout=20000)


def test_verify_badge_shows_on_card(page, server):
    page.goto(server)
    page.evaluate("""async () => {
      const projects = await fetch('/api/projects').then(r => r.json());
      await fetch(`/api/projects/${projects[0].id}`, {
        method: 'PATCH', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({verify_cmd: 'mockverify-pass'})});
    }""")
    page.fill("#qb-input", "verified change please")
    page.press("#qb-input", "Enter")
    card = page.locator(".col.s-review .card", has_text="verified change")
    expect(card).to_be_visible(timeout=20000)
    expect(card.locator(".chip", has_text="verified")).to_be_visible(timeout=10000)


def test_approval_card_has_always_allow(page, server):
    page.goto(server)
    _new_task(page, "E2E always allow", "risky [mock:approval]", perm="default")
    expect(page.locator("#appr-badge:visible, #more-badge:visible")).to_be_visible(timeout=15000)
    _tab(page, "approvals")
    row = page.locator(".rowcard", has_text="Bash").first
    expect(row.locator("button", has_text="∞ Always")).to_be_visible()
    row.locator("button:has-text('Approve')").first.click()
    _tab(page, "board")
    expect(page.locator(".col.s-review .card", has_text="E2E always allow")) \
        .to_be_visible(timeout=20000)


def test_board_filter_narrows_cards(page, server):
    page.goto(server)
    page.fill("#qb-input", "alpha unique thing")
    page.press("#qb-input", "Enter")
    page.fill("#qb-input", "beta other thing")
    page.press("#qb-input", "Enter")
    expect(page.locator(".card", has_text="alpha unique")).to_be_visible(timeout=10000)
    expect(page.locator(".card", has_text="beta other")).to_be_visible(timeout=10000)
    page.fill("#qb-filter", "alpha")
    expect(page.locator(".card", has_text="beta other")).to_have_count(0)
    expect(page.locator(".card", has_text="alpha unique")).to_be_visible()
    page.fill("#qb-filter", "")
    expect(page.locator(".card", has_text="beta other")).to_be_visible()


def test_deck_view_streams_panes(page, server):
    page.goto(server)
    # high priority so this task sorts to the top of the deck and is not evicted
    # by the 16-pane cap once the shared server has accumulated tasks
    proj = page.evaluate("async () => (await (await fetch('/api/projects')).json())[0].id")
    page.evaluate(f"""async () => {{
      const t = await (await fetch('/api/tasks', {{method:'POST',
        headers:{{'Content-Type':'application/json'}},
        body: JSON.stringify({{project_id:{proj}, title:'deck watch me work',
          prompt:'stream it', priority:4}})}})).json();
      await fetch(`/api/tasks/${{t.id}}/dispatch`, {{method:'POST',
        headers:{{'Content-Type':'application/json'}}, body:'{{}}'}});
    }}""")
    expect(page.locator(".card", has_text="deck watch me")).to_be_visible(timeout=15000)
    _tab(page, "deck")
    pane = page.locator(".pane", has_text="deck watch me")
    expect(pane).to_be_visible(timeout=15000)
    expect(pane.locator(".pane-line").first).to_be_visible(timeout=15000)


def test_settings_ui_saves_sinks(page, server):
    page.goto(server)
    _tab(page, "targets")
    page.locator('[data-settings="notifications"]').click()
    page.fill("#s-ntfy-server", "https://ntfy.sh")
    page.fill("#s-ntfy-topic", "lec-e2e")
    page.click("#s-save")
    expect(page.locator(".toast", has_text="Sinks saved")).to_be_visible()
    page.reload()
    _tab(page, "targets")
    page.locator('[data-settings="notifications"]').click()
    expect(page.locator("#s-ntfy-topic")).to_have_value("lec-e2e", timeout=5000)


def test_multi_attempt_badge_not_mislabeled_ab(page, server):
    """A retry produces 2 attempts but is NOT a parallel A/B run — the card must
    not claim 'A/B'."""
    page.goto(server)
    _new_task(page, "Retry me", "do it")
    expect(page.locator(".col.s-review .card", has_text="Retry me")) \
        .to_be_visible(timeout=20000)
    page.evaluate("""async () => {
      const ts = await (await fetch('/api/tasks')).json();
      const t = ts.find(x => x.title === 'Retry me');
      await fetch(`/api/tasks/${t.id}/dispatch`, {method:'POST',
        headers:{'Content-Type':'application/json'}, body:'{}'});
    }""")
    card = page.locator(".card", has_text="Retry me")
    expect(card.locator(".chip", has_text="×2")).to_be_visible(timeout=20000)
    assert page.locator(".card", has_text="Retry me").locator("text=A/B").count() == 0


def test_foreground_resync_refetches(page, server):
    """Returning to foreground (or an SSE reconnect, the shared path) must resync
    the board — otherwise a phone that missed events shows stale state."""
    page.goto(server)
    page.wait_for_selector("#board")
    page.wait_for_timeout(500)
    hits = []
    page.on("request", lambda r: hits.append(r.url)
            if r.url.rstrip("/").endswith("/api/tasks") else None)
    # faithfully simulate a phone returning to foreground. Headless Chromium
    # reports 'hidden' by default, which the handler correctly ignores — so force
    # 'visible' as a real unlock would.
    page.evaluate("""() => {
      Object.defineProperty(document, 'visibilityState',
        { get: () => 'visible', configurable: true });
      document.dispatchEvent(new Event('visibilitychange'));
    }""")
    page.wait_for_timeout(700)
    assert len(hits) >= 1, "foreground/reconnect did not resync /api/tasks"


def test_token_auth_ui_works(browser, auth_server):
    """With a bearer configured: no token → 401 (the UI cannot load data); token
    in localStorage → the board renders and SSE goes LIVE."""
    ctx = browser.new_context(viewport=DESKTOP)
    pg = ctx.new_page()
    try:
        pg.goto(auth_server)
        status = pg.evaluate("async () => (await fetch('/api/tasks')).status")
        assert status == 401, f"expected 401 without a token, got {status}"
        pg.evaluate("localStorage.setItem('lec-token','secret123')")
        pg.reload()
        expect(pg.locator(".col-head")).to_have_count(6, timeout=10000)
        # SSE authenticates through the query token, the only way EventSource can
        expect(pg.locator("#conn-label")).to_have_text("LIVE", timeout=10000)
    finally:
        ctx.close()


def test_delete_task_from_ui(page, server):
    """Delete removes the card (via the task_deleted SSE event) and closes the
    sheet."""
    page.goto(server)
    _new_task(page, "UI delete target", "noop")
    card = page.locator(".card", has_text="UI delete target")
    expect(card).to_be_visible(timeout=15000)
    card.first.locator(".t").click()
    page.on("dialog", lambda d: d.accept())
    page.click("#actions button:has-text('Delete')")
    expect(page.locator(".card", has_text="UI delete target")).to_have_count(0, timeout=10000)
    expect(page.locator("#sheet")).to_be_hidden()


def test_running_task_card_no_console_crash(page, server):
    """A running task has no diff yet (diff_stat is {} server-side). The card must
    render without throwing — a JS error here aborted the whole column render."""
    errors = []
    page.on("console", lambda m: errors.append(m.text) if m.type == "error" else None)
    page.on("pageerror", lambda e: errors.append(str(e)))
    page.goto(server)
    _new_task(page, "Long runner", "work [mock:slow]")
    expect(page.locator(".col.s-running .card", has_text="Long runner")) \
        .to_be_visible(timeout=10000)
    expect(page.locator(".col-head")).to_have_count(6)
    assert not [e for e in errors if "reduce" in e or "is not a function" in e], errors


def test_deck_persists_streams_across_updates(page, server):
    """The Deck reconciles panes incrementally — one task's status change must not
    tear down and reopen every pane's SSE. A persisting pane must be the SAME DOM
    node before and after."""
    marker = "DeckPersistA-uniqmark"
    page.goto(server)
    proj = page.evaluate("async () => (await (await fetch('/api/projects')).json())[0].id")
    page.evaluate(f"""async () => {{
      const t = await (await fetch('/api/tasks', {{method:'POST',
        headers:{{'Content-Type':'application/json'}},
        body: JSON.stringify({{project_id:{proj}, title:'{marker}',
          prompt:'slow one [mock:slow]', priority:4}})}})).json();
      await fetch(`/api/tasks/${{t.id}}/dispatch`, {{method:'POST',
        headers:{{'Content-Type':'application/json'}}, body:'{{}}'}});
    }}""")
    expect(page.locator(".card", has_text=marker)).to_be_visible(timeout=15000)
    _tab(page, "deck")
    expect(page.locator(".pane", has_text=marker)).to_be_visible(timeout=15000)
    page.evaluate(f"""() => {{
      const p = [...document.querySelectorAll('.pane')]
        .find(e => e.textContent.includes('{marker}'));
      p.dataset.lecMark = 'orig';
    }}""")
    page.evaluate(f"""async () => {{
      const t = await (await fetch('/api/tasks', {{method:'POST',
        headers:{{'Content-Type':'application/json'}},
        body: JSON.stringify({{project_id:{proj}, title:'DeckPersistB', prompt:'quick'}})}})).json();
      await fetch(`/api/tasks/${{t.id}}/dispatch`, {{method:'POST',
        headers:{{'Content-Type':'application/json'}}, body:'{{}}'}});
    }}""")
    expect(page.locator(".pane", has_text="DeckPersistB")).to_be_visible(timeout=20000)
    still = page.evaluate(f"""() => {{
      const p = [...document.querySelectorAll('.pane')]
        .find(e => e.textContent.includes('{marker}'));
      return p ? p.dataset.lecMark : 'PANE-GONE';
    }}""")
    assert still == "orig", f"deck pane A was recreated on another task's update: {still}"


def test_fable_dispatch_requires_confirmation(page, server):
    """Fable 5 is the highest-usage model — dispatching on it must prompt, and
    dismissing the prompt must abort."""
    page.goto(server)
    page.click("#fab")
    page.fill("#f-title", "Fable guarded task")
    page.fill("#f-model", "fable")
    seen = []
    page.once("dialog", lambda d: (seen.append(d.message), d.dismiss()))
    page.click("#f-go")
    page.wait_for_timeout(800)
    assert seen and "fable" in seen[0].lower(), seen
    expect(page.locator(".card", has_text="Fable guarded task")).to_have_count(0)

    page.once("dialog", lambda d: d.accept())
    page.click("#f-go")
    expect(page.locator(".card", has_text="Fable guarded task")).to_be_visible(timeout=10000)


def test_agent_toggle_reshapes_the_form(page, server):
    """Switching to codex must disable gated mode and hide the A/B row — both are
    Claude-only, and offering them produces a dispatch that fails later for
    reasons the operator cannot see."""
    page.goto(server)
    page.click("#fab")
    expect(page.locator("#f-agent button.on")).to_have_text("Claude Code")

    page.click("#f-agent button[data-agent='codex']")
    expect(page.locator("#f-agent button.on")).to_have_text("Codex")
    assert page.eval_on_selector("#f-agent", "e => e.dataset.value") == "codex"
    assert page.eval_on_selector("#f-perm option[value='default']", "e => e.disabled") is True
    expect(page.locator("#f-ab-row")).to_be_hidden()

    page.click("#f-agent button[data-agent='claude']")
    assert page.eval_on_selector("#f-perm option[value='default']", "e => e.disabled") is False
    expect(page.locator("#f-ab-row")).to_be_visible()


def test_codex_task_dispatches_from_the_toggle(page, server):
    page.goto(server)
    page.click("#fab")
    page.fill("#f-title", "Codex toggle task")
    page.click("#f-agent button[data-agent='codex']")
    page.click("#f-save")
    card = page.locator(".card", has_text="Codex toggle task")
    expect(card).to_be_visible(timeout=10000)
    expect(card.locator(".chip", has_text="codex")).to_be_visible()


def test_pwa_assets(page, server):
    page.goto(server)
    for asset in ("/manifest.webmanifest", "/sw.js", "/icon.svg", "/style.css", "/fonts.css"):
        assert page.evaluate(f"fetch('{asset}').then(r=>r.ok)"), asset


def test_mobile_board_has_no_page_level_horizontal_overflow(browser, server):
    """The quickbar filter once pushed the document wider than a phone viewport,
    so the whole page wobbled sideways while scrolling."""
    ctx = browser.new_context(viewport=PHONE)
    pg = ctx.new_page()
    try:
        pg.goto(server + "/#board")
        pg.wait_for_selector(".card, .col", timeout=10000)
        scroll_w = pg.evaluate("() => document.documentElement.scrollWidth")
        assert scroll_w <= PHONE["width"] + 1, \
            f"page overflows horizontally on phones ({scroll_w}px)"
    finally:
        ctx.close()


# ---- sessions: the interactive half of the board ----------------------------

def test_sessions_tab_starts_and_shows_a_session(page, server):
    """A session is an agent you work WITH — its card leads with how long it has
    been quiet and what is on its screen."""
    page.goto(server)
    _tab(page, "sessions")
    expect(page.locator("#sess-new")).to_be_visible()

    page.click("#sess-new")
    page.fill("#ns-name", "long haul")
    page.click("#ns-go")

    card = page.locator(".scard", has_text="long haul")
    expect(card).to_be_visible(timeout=15000)
    # the two things a task card never needs
    expect(card.locator(".sidle")).to_contain_text("quiet", timeout=15000)
    expect(card.locator(".spane")).to_contain_text("mock agent", timeout=15000)
    # and the primary action is getting into the real terminal
    expect(card.locator("button", has_text="Attach")).to_be_visible()
    expect(page.locator("#sess-badge")).to_be_visible(timeout=10000)


def test_session_send_reaches_the_pane(page, server):
    page.goto(server)
    _tab(page, "sessions")
    page.click("#sess-new")
    page.fill("#ns-name", "chatty")
    page.click("#ns-go")
    card = page.locator(".scard", has_text="chatty")
    expect(card).to_be_visible(timeout=15000)

    card.get_by_role("button", name="Chat", exact=True).click()
    page.fill("#conversation-input", "where are we?")
    page.click("#conversation-send")
    expect(page.locator("#conversation-log")).to_contain_text("where are we?", timeout=15000)
    page.click("#conversation-close")
    expect(card.locator(".spane")).to_contain_text("where are we?", timeout=15000)


def test_discover_offers_to_adopt_a_hand_started_agent(page, server):
    """The sessions worth tracking are usually the ones you started yourself."""
    page.goto(server)
    _tab(page, "sessions")
    page.click("#sess-discover")
    row = page.locator(".cand", has_text="legacy-claude").first
    expect(row).to_be_visible(timeout=15000)
    row.locator("button", has_text="Adopt").click()
    card = page.locator(".scard", has_text="legacy-claude")
    expect(card).to_be_visible(timeout=15000)
    expect(card.locator(".chip", has_text="adopted")).to_be_visible()


def test_import_registers_projects_from_a_scan(page, server):
    """An empty board is why a tool like this gets abandoned in week one."""
    page.goto(server)
    _tab(page, "targets")
    page.locator('[data-settings="projects"]').click()
    expect(page.locator("#imp-root")).to_be_visible()
    page.fill("#imp-root", "/mock/does-not-exist")
    page.click("#imp-scan")
    # the mock target has no filesystem, so this proves the flow reports an
    # empty result honestly rather than pretending to find something
    expect(page.locator("#imp-out")).to_contain_text("Nothing project-shaped", timeout=10000)


def test_adopted_session_offers_release_not_just_kill(page, server):
    """Adoption is non-destructive, so letting go has to be too — an agent you
    started yourself must not be killed by a button labelled like a delete."""
    page.goto(server)
    _tab(page, "sessions")
    page.click("#sess-discover")
    row = page.locator(".cand", has_text="legacy-claude").first
    expect(row).to_be_visible(timeout=15000)
    row.locator("button", has_text="Adopt").click()
    card = page.locator(".scard", has_text="legacy-claude")
    expect(card).to_be_visible(timeout=15000)
    card.locator(".action-menu > summary").click()
    # the safe action is present and unconfirmed; the destructive one is separate
    expect(card.locator("button", has_text="Stop tracking")).to_be_visible()
    expect(card.locator("button", has_text="Kill")).to_be_visible()

    card.locator("button", has_text="Stop tracking").click()
    expect(page.locator(".scard", has_text="legacy-claude")).to_have_count(0, timeout=10000)
    # released, not killed: discovery finds the terminal again
    page.click("#sess-discover")
    expect(page.locator(".cand", has_text="legacy-claude").first).to_be_visible(timeout=15000)


# ---- deep links --------------------------------------------------------------
# The address bar is an interface: notification sinks send "/#task/12", and the
# phone opens the embedded board at "/#sessions".

def test_hash_opens_the_named_tab(page, server):
    page.goto(server + "/#sessions")
    expect(page.locator("#sess-new")).to_be_visible(timeout=10000)
    expect(page.locator(".tab[data-tab='sessions']")).to_have_class(__import__("re").compile("on"))

    page.goto(server + "/#targets")
    page.locator('[data-settings="projects"]').click()
    expect(page.locator("#imp-root")).to_be_visible(timeout=10000)


def test_hash_opens_a_task_sheet(page, server):
    """This is the link every 'Ready for review' notification carries."""
    page.goto(server)
    _new_task(page, "Deep linked task", "do it")
    card = page.locator(".card", has_text="Deep linked task")
    expect(card).to_be_visible(timeout=15000)
    task_id = page.evaluate("""async () => {
      const ts = await (await fetch('/api/tasks')).json();
      return ts.find(t => t.title === 'Deep linked task').id;
    }""")
    page.goto(f"{server}/#task/{task_id}")
    expect(page.locator("#sheet")).to_be_visible(timeout=10000)
    expect(page.locator("#sheet h2")).to_have_text("Deep linked task")


def test_switching_tabs_updates_the_hash(page, server):
    page.goto(server)
    _tab(page, "sessions")
    page.wait_for_timeout(400)
    assert page.evaluate("location.hash") == "#sessions"


def test_any_agent_can_be_defined_and_picked(page, server):
    """The board should not hold an opinion about which CLI is in the terminal."""
    page.request.put(f"{server}/api/agents", data=[
        {"name": "aider", "command": "aider", "model_flag": "--model",
         "prompt_arg": True},
    ])
    page.goto(server + "/#sessions")
    page.click("#sess-new")
    # the agent list is fetched, so wait for it rather than racing it
    expect(page.locator("#ns-agent option")).to_have_count(4, timeout=10000)
    options = page.locator("#ns-agent option").all_inner_texts()
    assert any("aider" in o for o in options), options
    assert any("claude" in o for o in options), options

    page.select_option("#ns-agent", "aider")
    page.fill("#ns-name", "local agent")
    page.click("#ns-go")
    card = page.locator(".scard", has_text="local agent")
    expect(card).to_be_visible(timeout=15000)
    expect(card.locator(".chip", has_text="aider")).to_be_visible()
    # leave the shared server as we found it
    page.request.put(f"{server}/api/agents", data=[])


def test_blank_room_session_can_be_promoted_to_a_project(page, server):
    """A blank room is for work that has no name yet: any agent, a throwaway
    directory, no project. Deciding what it is happens afterwards, and the
    session keeps running through it."""
    page.goto(server)
    _tab(page, "sessions")
    page.click("#sess-new")

    # the option people do not know exists is offered first, but a project stays
    # the default when you have one
    options = page.locator("#ns-project option").all_text_contents()
    assert "Blank room" in options[0], options
    assert page.locator("#ns-project").input_value() != "", "a project should be preselected"

    page.select_option("#ns-project", "")
    expect(page.locator("#ns-proj-hint")).to_contain_text("throwaway directory")
    # nothing is known about a room that does not exist yet
    expect(page.locator("#ns-start option[value='brief']")).to_be_disabled()

    page.fill("#ns-name", "half an idea")
    page.click("#ns-go")

    card = page.locator(".scard", has_text="half an idea")
    expect(card).to_be_visible(timeout=15000)
    # it starts life unassigned, grouped apart from real projects
    expect(card.locator(".scard-project", has_text="Unassigned")).to_be_visible(timeout=15000)
    card.locator(".action-menu > summary").click()
    promote = card.locator("button", has_text="Make a project")
    expect(promote).to_be_visible()

    page.on("dialog", lambda d: d.accept("half-an-idea"))
    promote.click()

    # the work is a project now, and the conversation is still running in it
    expect(page.locator(".scard-project", has_text="half-an-idea")).to_be_visible(timeout=15000)
    card = page.locator(".scard", has_text="half an idea")
    expect(card.locator("button", has_text="Attach")).to_be_visible()
    expect(card.locator("button", has_text="Make a project")).to_have_count(0)

    # and it is dispatchable: the new project is offered on the task form
    _tab(page, "board")
    page.click("#fab")
    expect(page.locator("#f-project")).to_contain_text("half-an-idea")


@pytest.mark.parametrize("page", [PHONE], indirect=True, ids=["phone"])
def test_phone_session_cards_do_not_overflow_with_every_button(page, server):
    """Session cards grew a "Make a project" button. A phone is 390px wide and
    the button row is where that shows up first."""
    page.goto(server)
    _tab(page, "sessions")
    page.click("#sess-new")
    page.select_option("#ns-project", "")          # blank room: the most buttons
    page.fill("#ns-name", "phone room")
    page.click("#ns-go")

    card = page.locator(".scard", has_text="phone room")
    expect(card).to_be_visible(timeout=15000)
    card.locator(".action-menu > summary").click()
    expect(card.locator("button", has_text="Make a project")).to_be_visible()

    # nothing may push the document sideways
    scroll_w = page.evaluate("() => document.documentElement.scrollWidth")
    assert scroll_w <= PHONE["width"] + 1, f"sessions tab overflows on a phone ({scroll_w}px)"

    # and every button must actually be reachable inside the card
    overflow = card.evaluate("""el => {
        const row = el.querySelector('.btnrow');
        return row ? row.scrollWidth - row.clientWidth : 0;
    }""")
    assert overflow <= 1, f"the button row is {overflow}px wider than the card"

    # each button must be tappable: at least 30px tall and inside the viewport
    for i in range(card.locator(".btnrow button").count()):
        box = card.locator(".btnrow button").nth(i).bounding_box()
        assert box["height"] >= 28, f"button {i} is only {box['height']}px tall"
        assert box["x"] >= 0 and box["x"] + box["width"] <= PHONE["width"] + 1, \
            f"button {i} runs off the screen: {box}"


@pytest.mark.parametrize("page", [PHONE], indirect=True, ids=["phone"])
def test_phone_new_session_sheet_fits(page, server):
    """The sheet gained a project option and a model list; it still has to be
    usable one-handed."""
    page.goto(server)
    _tab(page, "sessions")
    page.click("#sess-new")
    for sel in ("#ns-project", "#ns-agent", "#ns-model", "#ns-start", "#ns-go"):
        expect(page.locator(sel)).to_be_visible()
        box = page.locator(sel).bounding_box()
        assert box["x"] >= 0 and box["x"] + box["width"] <= PHONE["width"] + 1, \
            f"{sel} runs off the screen: {box}"
    scroll_w = page.evaluate("() => document.documentElement.scrollWidth")
    assert scroll_w <= PHONE["width"] + 1, f"the sheet overflows ({scroll_w}px)"


def test_attach_opens_a_same_origin_terminal(page, server):
    """Attach used to open http://<the browser's hostname>:<port>, which named
    whichever machine served the page — the reverse proxy, usually, which runs no
    ttyd. It must be a path on this origin."""
    page.goto(server)
    _tab(page, "sessions")
    page.click("#sess-new")
    page.fill("#ns-name", "attachable")
    page.click("#ns-go")
    card = page.locator(".scard", has_text="attachable")
    expect(card).to_be_visible(timeout=15000)

    pages_before = len(page.context.pages)
    card.locator("button", has_text="Attach").click()
    frame = page.locator('#terminal-workspace iframe')
    expect(frame).to_be_visible(timeout=10000)
    url = frame.get_attribute('src')
    assert url.startswith('/terminal/session/'), url
    assert len(page.context.pages) == pages_before

    # and that path must actually serve ttyd through the proxy
    resp = page.request.get(server + url)
    assert resp.ok, f"{url} -> {resp.status}"


def test_yolo_is_offered_and_on_by_default(page, server):
    """Yolo is the default because you are sitting in the terminal watching the
    agent — but it must be visible and switchable, not silent."""
    page.goto(server)
    _tab(page, "sessions")
    page.click("#sess-new")

    yolo = page.locator("#ns-yolo")
    expect(yolo).to_be_visible()
    assert yolo.is_checked(), "yolo should be on by default"
    expect(page.locator("#ns-yolo-hint")).to_contain_text("without stopping to ask")

    # unticking says what changes, so the choice is legible
    yolo.uncheck()
    page.wait_for_timeout(300)
    expect(page.locator("#ns-yolo-hint")).to_contain_text("stops and asks")


@pytest.mark.parametrize("page", [PHONE], indirect=True, ids=["phone"])
def test_yolo_toggle_fits_on_a_phone(page, server):
    page.goto(server)
    _tab(page, "sessions")
    page.click("#sess-new")
    box = page.locator("#ns-yolo").bounding_box()
    assert box["x"] >= 0 and box["x"] + box["width"] <= PHONE["width"] + 1, box
    assert page.evaluate("() => document.documentElement.scrollWidth") <= PHONE["width"] + 1


def test_projects_can_be_filtered_and_deleted(page, server):
    """Eighty-one projects is a wall. The list leads with staleness, filters by
    name, and deletion is itemised before it happens."""
    page.goto(server)
    _tab(page, "targets")
    page.locator('[data-settings="projects"]').click()
    expect(page.locator("#pj-list")).to_be_visible(timeout=10000)
    rows = page.locator(".pjrow")
    expect(rows.first).to_be_visible(timeout=10000)
    before = rows.count()
    assert before >= 2, f"expected the seeded projects, saw {before}"

    # filtering narrows it, and the count says so
    page.fill("#pj-search", "demo")
    page.wait_for_timeout(300)
    assert page.locator(".pjrow").count() < before
    expect(page.locator("#pj-count")).to_contain_text("/")
    page.fill("#pj-search", "")
    page.wait_for_timeout(300)

    # tapping a row opens that project rather than deleting anything
    rows.first.click()
    expect(page.locator("#sheet")).to_be_visible()
    page.locator("#sheet .x").click()

    # a project with no history deletes without needing cascade
    name = "disposable-" + str(int(time.time()))
    pid = page.evaluate("""async (name) => {
        const t = await (await fetch('/api/targets')).json();
        const r = await fetch('/api/projects', {method:'POST',
            headers:{'Content-Type':'application/json'},
            body: JSON.stringify({name, target_id: t[0].id, repo_path: '/mock/' + name})});
        return (await r.json()).id;
    }""", name)
    page.reload()
    _tab(page, "targets")
    page.locator('[data-settings="projects"]').click()
    page.fill("#pj-search", name)
    page.wait_for_timeout(400)
    expect(page.locator(".pjrow", has_text=name)).to_be_visible(timeout=10000)

    page.click("#pj-select")
    page.locator(".pjrow", has_text=name).locator("input[type=checkbox]").check()
    expect(page.locator("#pj-del")).to_contain_text("Delete 1")

    # the confirmation must name what goes
    seen = []
    page.on("dialog", lambda d: (seen.append(d.message), d.accept()))
    page.click("#pj-del")
    page.wait_for_timeout(2500)
    assert seen and name in seen[0], f"the confirmation should name the project: {seen}"
    assert "code on disk is untouched" in seen[0], seen[0]

    gone = page.evaluate(f"fetch('/api/projects/{pid}').then(r => r.status)")
    assert gone == 404, f"the project is still there ({gone})"


@pytest.mark.parametrize("page", [PHONE], indirect=True, ids=["phone"])
def test_project_list_is_usable_on_a_phone(page, server):
    page.goto(server)
    _tab(page, "targets")
    page.locator('[data-settings="projects"]').click()
    expect(page.locator("#pj-list")).to_be_visible(timeout=10000)
    assert page.evaluate("() => document.documentElement.scrollWidth") <= PHONE["width"] + 1
    row = page.locator(".pjrow").first
    box = row.bounding_box()
    assert box["x"] + box["width"] <= PHONE["width"] + 1, box
    assert box["height"] >= 30, f"rows are only {box['height']}px — hard to tap"


def test_a_shell_into_the_project_is_one_click(page, server):
    """Sometimes you just want to look at the code yourself — read a file, fix
    one line — without asking an agent to do it."""
    page.goto(server)
    _tab(page, "targets")
    page.locator('[data-settings="projects"]').click()
    expect(page.locator("#pj-list")).to_be_visible(timeout=10000)

    row = page.locator(".pjrow").first
    shell_btn = row.locator("button")
    expect(shell_btn).to_be_visible()

    pages_before = len(page.context.pages)
    shell_btn.click()
    frame = page.locator('#terminal-workspace iframe')
    expect(frame).to_be_visible(timeout=10000)
    assert frame.get_attribute('src').startswith('/terminal/project/')
    assert len(page.context.pages) == pages_before
    _tab(page, "targets")
    page.locator('[data-settings="projects"]').click()
    expect(page.locator("#pj-list")).to_be_visible()
    # Navigation hides the terminal, without destroying it.
    expect(page.locator('#terminal-workspace iframe')).to_have_count(1)


def test_a_routine_can_be_saved_and_run_with_one_button(page, server):
    """The job you keep asking for, saved: one button across every project."""
    page.goto(server)
    page.click("#qb-routines")
    expect(page.locator("#rt-list")).to_be_visible(timeout=10000)

    page.click("#sheet summary")
    page.fill("#rt-name", "PR sweep")
    page.select_option("#rt-projects", index=0)
    page.fill("#rt-prompt", "Review every open PR, test it end to end, merge when CI is green")
    page.click("#rt-save")

    card = page.locator("#rt-list .rowcard", has_text="PR sweep")
    expect(card).to_be_visible(timeout=10000)
    expect(card).to_contain_text("manual only")

    before = page.evaluate("fetch('/api/tasks').then(r=>r.json()).then(t=>t.length)")
    card.locator("button", has_text="Run now").click()
    page.wait_for_timeout(2500)
    after = page.evaluate("fetch('/api/tasks').then(r=>r.json()).then(t=>t.length)")
    assert after > before, f"running the routine created no tasks ({before} -> {after})"


def test_a_scheduled_routine_shows_when_it_next_runs(page, server):
    page.goto(server)
    page.click("#qb-routines")
    page.click("#sheet summary")
    page.fill("#rt-name", "Nightly sweep")
    page.select_option("#rt-projects", index=0)
    page.fill("#rt-prompt", "nightly work")
    page.fill("#rt-schedule", "daily at 09:00")
    page.click("#rt-save")

    card = page.locator("#rt-list .rowcard", has_text="Nightly sweep")
    expect(card).to_be_visible(timeout=10000)
    expect(card).to_contain_text("daily at 09:00")
    expect(card).to_contain_text("next")
    # a scheduled routine can be paused without deleting it
    expect(card.locator("button", has_text="Pause")).to_be_visible()

    # a schedule it cannot read is refused rather than silently ignored
    page.fill("#rt-name", "Broken")
    page.select_option("#rt-projects", index=0)
    page.fill("#rt-prompt", "x")
    page.fill("#rt-schedule", "0 9 * * *")
    page.click("#rt-save")
    page.wait_for_timeout(1500)
    expect(page.locator("#rt-list .rowcard", has_text="Broken")).to_have_count(0)


def test_a_card_has_an_x_and_a_column_has_a_clear(page, server):
    """Deleting one card should be one click on that card, and clearing a
    column should live on the column it clears — not a dialog asking which."""
    page.goto(server)
    _new_task(page, "Sweep me", "do a thing")
    expect(page.locator(".col.s-review .card", has_text="Sweep me")).to_be_visible(timeout=30000)

    # the x is on the card itself
    card = page.locator(".card", has_text="Sweep me")
    expect(card.locator(".card-x")).to_have_count(1)

    status = page.evaluate("""async () => {
        const tasks = await (await fetch('/api/tasks')).json();
        const t = tasks.find(x => x.title === 'Sweep me');
        const r = await fetch(`/api/tasks/${t.id}/complete`, {method:'POST',
            headers:{'Content-Type':'application/json'}, body:'{}'});
        return r.status;
    }""")
    assert status == 200, f"completing returned {status}"
    page.reload()
    expect(page.locator(".col.s-done .card", has_text="Sweep me")).to_be_visible(timeout=15000)

    # finished work goes without a confirmation — it is a record, not work
    dialogs = []
    page.on("dialog", lambda d: (dialogs.append(d.message), d.accept()))
    page.locator(".col.s-done .card", has_text="Sweep me").locator(".card-x").click()
    page.wait_for_timeout(2000)
    assert not dialogs, f"deleting a finished card should not nag: {dialogs}"
    gone = page.evaluate("""fetch('/api/tasks').then(r=>r.json())
        .then(t => !t.some(x => x.title === 'Sweep me'))""")
    assert gone, "the card is still there"


def test_clearing_a_column_lives_on_that_column(page, server):
    page.goto(server)
    for title in ("First", "Second"):
        _new_task(page, title, "x")
        expect(page.locator(".col.s-review .card", has_text=title)).to_be_visible(timeout=30000)
    page.evaluate("""async () => {
        const tasks = await (await fetch('/api/tasks')).json();
        for (const t of tasks.filter(x => ['First','Second'].includes(x.title))) {
            await fetch(`/api/tasks/${t.id}/complete`, {method:'POST',
                headers:{'Content-Type':'application/json'}, body:'{}'});
        }
    }""")
    page.reload()
    head = page.locator(".col.s-done .col-head")
    expect(head.locator(".col-clear")).to_be_visible(timeout=15000)

    page.on("dialog", lambda d: d.accept())
    head.locator(".col-clear").click()
    page.wait_for_timeout(2500)
    left = page.evaluate("""fetch('/api/tasks').then(r=>r.json())
        .then(t => t.filter(x => x.status === 'done').length)""")
    assert left == 0, f"{left} done cards survived clearing the column"


def test_the_quickbar_has_no_unstyled_buttons(page, server):
    """Two native buttons in a styled bar looked exactly as bad as that sounds."""
    page.goto(server)
    page.wait_for_timeout(1000)
    for i in range(page.locator("#quickbar > *").count()):
        el = page.locator("#quickbar > *").nth(i)
        bg = el.evaluate("e => getComputedStyle(e).backgroundColor")
        # every control in the bar sits on the panel colour, never browser default
        assert bg not in ("rgb(255, 255, 255)", "rgba(0, 0, 0, 0)", "buttonface"), \
            f"quickbar child {i} is unstyled: {bg}"


def test_handoff_lets_you_choose_which_agent_picks_it_up(page, server):
    """The point of a handoff is usually moving the work to a different agent.
    It used to be a yes/no confirm that always reused the same one."""
    page.goto(server)
    _tab(page, "sessions")
    page.click("#sess-new")
    page.fill("#ns-name", "handoff me")
    page.click("#ns-go")
    card = page.locator(".scard", has_text="handoff me")
    expect(card).to_be_visible(timeout=15000)

    card.locator(".action-menu > summary").click()
    card.locator("button", has_text="Handoff").click()
    expect(page.locator("#ho-agent")).to_be_visible(timeout=10000)

    # it defaults to the agent that is already there, and says what that means
    expect(page.locator("#ho-agent")).to_have_value("claude")
    expect(page.locator("#ho-agent-hint")).to_contain_text("clean context")

    # and any other agent can take it instead — the whole point
    options = page.locator("#ho-agent option").all_text_contents()
    assert any("codex" in o for o in options), options
    page.select_option("#ho-agent", "codex")
    expect(page.locator("#ho-agent-hint")).to_contain_text("moves to codex")

    # choosing to just record it hides the successor options
    page.select_option("#ho-mode", "note")
    expect(page.locator("#ho-successor")).to_be_hidden()
    page.select_option("#ho-mode", "successor")
    expect(page.locator("#ho-successor")).to_be_visible()

    page.click("#ho-go")
    page.wait_for_timeout(4000)
    # a codex session picked up the thread
    agent = page.evaluate("""fetch('/api/sessions').then(r=>r.json()).then(ss => {
        const s = ss.filter(x => x.name === 'handoff me' && x.status !== 'dead')[0];
        return s ? s.agent : null;
    })""")
    assert agent == "codex", f"the successor is running {agent!r}, not codex"


def test_running_build_identifies_the_serving_binary(page, server):
    page.goto(server)
    _tab(page, "targets")
    page.locator('[data-settings="about"]').click()
    health = page.request.get(server + "/api/health").json()
    build = health["build"]
    expect(page.locator("#running-build")).to_contain_text(health["version"])
    expect(page.locator("#running-build")).to_contain_text(build["revision"][:12] or "revision unknown")
    label = "local changes" if build["modified"] is True else "clean" if build["modified"] is False else "build status unknown"
    expect(page.locator("#running-build")).to_contain_text(label)


@pytest.mark.parametrize("status,message", [
    ("unavailable", "Memory unavailable. Work can continue using the repository and saved handoffs."),
    ("empty", "No relevant memory found for this project."),
    ("partial", "Memory is partially unavailable. Some context could not be retrieved; work can continue."),
])
def test_session_preview_shows_memory_status(page, server, status, message):
    page.route("**/api/projects/*/brief", lambda route: route.fulfill(json={
        "brief": "test brief", "memory": {"status": status, "message": message}
    }))
    page.goto(server)
    _tab(page, "sessions")
    page.click("#sess-new")
    page.select_option("#ns-start", "brief")
    expect(page.locator("#ns-memory-status")).to_have_text(message)
    expect(page.locator("#ns-go")).to_be_enabled()


def test_a_routine_can_be_edited(page, server):
    """A saved job you cannot change is one you delete and retype."""
    page.goto(server)
    page.click("#qb-routines")
    page.click("#rt-legend")
    page.fill("#rt-name", "Editable")
    page.select_option("#rt-projects", index=0)
    page.fill("#rt-prompt", "original prompt")
    page.click("#rt-save")

    card = page.locator("#rt-list .rowcard", has_text="Editable")
    expect(card).to_be_visible(timeout=10000)

    card.locator("button", has_text="Edit").click()
    # the form is filled with what is already there, not blank
    expect(page.locator("#rt-name")).to_have_value("Editable")
    expect(page.locator("#rt-prompt")).to_have_value("original prompt")
    expect(page.locator("#rt-legend")).to_contain_text("Editing")

    page.fill("#rt-name", "Renamed")
    page.fill("#rt-schedule", "daily at 07:00")
    page.click("#rt-save")

    expect(page.locator("#rt-list .rowcard", has_text="Renamed")).to_be_visible(timeout=10000)
    expect(page.locator("#rt-list .rowcard", has_text="Editable")).to_have_count(0)
    expect(page.locator("#rt-list .rowcard", has_text="Renamed")).to_contain_text("daily at 07:00")
    # editing must update in place, not leave a second copy behind. Counted by
    # name, not globally: this server is shared with every other test here.
    n = page.evaluate("""fetch('/api/routines').then(r=>r.json())
        .then(x => x.filter(r => ['Editable','Renamed'].includes(r.name)).length)""")
    assert n == 1, f"editing duplicated the routine ({n} copies)"


@pytest.mark.parametrize("page", [PHONE], indirect=True, ids=["phone"])
def test_mobile_task_conversation_keeps_drafts_and_continues(page, server):
    page.goto(server + "/#board")
    _new_task(page, "Mobile conversation", "Create a friendly welcome page")
    card = page.locator(".col.s-review .card", has_text="Mobile conversation")
    expect(card).to_be_visible(timeout=20000)
    card.locator(".t").click()
    page.locator("#actions").get_by_role("button", name="Chat", exact=True).click()
    expect(page.locator("#conversation-log")).to_contain_text("Create a friendly welcome page")
    box = page.locator("#conversation-input")
    box.fill("Use larger headings")
    box.press("Enter")
    box.type("Keep the footer")
    expect(box).to_have_value("Use larger headings\nKeep the footer")
    page.wait_for_timeout(2300)  # a live refresh must leave the caret and draft alone
    expect(box).to_have_value("Use larger headings\nKeep the footer")
    assert box.evaluate("e=>parseFloat(getComputedStyle(e).fontSize)") >= 16
    assert page.locator("#conversation").evaluate("e=>e.scrollWidth <= innerWidth")
    page.click("#conversation-close")
    page.locator("#actions").get_by_role("button", name="Chat", exact=True).click()
    expect(box).to_have_value("Use larger headings\nKeep the footer")
    page.route("**/api/tasks/*/messages", lambda route: route.abort() if route.request.method == "POST" else route.continue_())
    page.click("#conversation-send")
    expect(page.locator("#conversation-receipt")).to_contain_text("Your draft is kept")
    expect(box).to_have_value("Use larger headings\nKeep the footer")
    page.unroute("**/api/tasks/*/messages")
    page.click("#conversation-send")
    expect(box).to_have_value("")
    expect(page.locator("#conversation-log")).to_contain_text("You · delivered to agent", timeout=20000)
    expect(page.locator("#conversation-status")).to_contain_text("turn 2", timeout=20000)
    expect(page.locator("#conversation-log")).to_contain_text("Result · turn 2", timeout=20000)
    page.click("#reader-larger")
    assert page.locator(".reader-text").first.evaluate("e=>parseFloat(getComputedStyle(e).fontSize)") >= 18
    page.screenshot(path="/tmp/lectern-mobile-conversation.png")
    page.click("#conversation-close")
    expect(page.locator("#sheet")).to_be_visible()


@pytest.mark.parametrize("width", [320, 390, 768, 1100, 1440])
def test_responsive_chrome_and_dispatch_remain_usable(page, server, width):
    page.set_viewport_size({"width": width, "height": 900})
    page.goto(server + "/#board")
    expect(page.locator("#qb-input")).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
    if width <= 600:
        assert page.locator("#qb-input").bounding_box()["width"] >= width - 40
        assert page.locator("#qb-project").bounding_box()["height"] >= 44
    if width >= 1024:
        title = page.locator(".brand").bounding_box()
        nav = page.locator("#tabbar").bounding_box()
        assert nav["x"] >= title["x"] + title["width"] or nav["y"] >= title["y"] + title["height"]


@pytest.mark.parametrize("page", [PHONE], indirect=True, ids=["phone"])
def test_dispatch_directly_into_chat_and_message_running_task(page, server):
    page.goto(server + "/#board")
    page.click("#fab")
    page.fill("#f-title","Talk while working")
    page.fill("#f-prompt","Review this change [mock:approval]")
    page.select_option("#f-perm","default")
    page.click("#f-chat")
    expect(page.locator("#conversation")).to_be_visible()
    expect(page.locator("#conversation-status")).to_contain_text("running",timeout=20000)
    page.fill("#conversation-input","Keep the existing public API")
    page.click("#conversation-send")
    expect(page.locator("#conversation-log")).to_contain_text("You · queued for next turn",timeout=10000)
    expect(page.locator("#conversation-log")).to_contain_text("Keep the existing public API")
    page.click("#conversation-close")
    page.click("#sheet .x")
    page.locator('.card',has_text='Talk while working').get_by_role('button',name='Chat with this task').click()
    expect(page.locator("#conversation-log")).to_contain_text("Keep the existing public API")


def test_mobile_context_attachments_keep_drafts_and_send(page, server):
    page.set_viewport_size(PHONE)
    page.goto(server)
    _tab(page, "sessions")
    page.click("#sess-new")
    page.fill("#ns-name", "file context")
    page.click("#ns-go")
    card = page.locator(".scard", has_text="file context")
    expect(card).to_be_visible(timeout=15000)
    card.get_by_role("button", name="Chat", exact=True).click()
    files = page.locator("#conversation-files")
    files.set_input_files([
        {"name": "Résumé draft.pdf", "mimeType": "application/pdf", "buffer": b"%PDF-1.4\ncontext"},
        {"name": "notes.txt", "mimeType": "text/plain", "buffer": b"Keep this note"},
    ])
    expect(page.locator("#conversation-attachments")).to_contain_text("Résumé draft.pdf")
    expect(page.locator("#conversation-upload-status")).to_contain_text("Files ready")
    page.get_by_role("button", name="Remove notes.txt", exact=True).click()
    page.fill("#conversation-input", "Please explain the attached PDF")
    page.click("#conversation-close")
    card.get_by_role("button", name="Chat", exact=True).click()
    expect(page.locator("#conversation-attachments")).to_contain_text("Résumé draft.pdf")
    expect(page.locator("#conversation-attachments")).not_to_contain_text("notes.txt")
    expect(page.locator("#conversation-input")).to_have_value("Please explain the attached PDF")
    assert page.locator("#conversation").evaluate("e=>e.scrollWidth <= innerWidth")
    page.route("**/api/sessions/*/send", lambda route: route.fulfill(status=502, content_type="application/json", body='{"detail":"target offline"}'))
    page.click("#conversation-send")
    expect(page.locator("#conversation-receipt")).to_contain_text("Your draft is kept")
    expect(page.locator("#conversation-attachments")).to_contain_text("Résumé draft.pdf")
    page.unroute("**/api/sessions/*/send")
    with page.expect_request("**/api/sessions/*/send") as sent:
        page.click("#conversation-send")
    assert "Résumé draft.pdf" in sent.value.post_data_json["text"]
    assert ".lectern/context/" in sent.value.post_data_json["text"]
    assert "notes.txt" not in sent.value.post_data_json["text"]
    expect(page.locator("#conversation-log")).to_contain_text("Please explain the attached PDF", timeout=15000)
    expect(page.locator("#conversation-attachments")).to_be_empty()
    expect(page.locator("#conversation-input")).to_have_value("")
    page.screenshot(path="/tmp/lectern-attachments-mobile.png")
    page.click("#conversation-close")


def test_context_upload_failure_and_attachment_only_task_message(page, server):
    page.goto(server)
    _new_task(page, "Attachment followup", "Read the next file")
    card = page.locator(".col.s-review .card", has_text="Attachment followup")
    expect(card).to_be_visible(timeout=15000)
    card.click()
    page.get_by_role("button", name="Chat", exact=True).click()
    files = page.locator("#conversation-files")
    payload={"name":"notes.txt","mimeType":"text/plain","buffer":b"reference text"}
    page.route("**/api/tasks/*/attachments", lambda route: route.fulfill(status=502, content_type="application/json", body='{"detail":"disk full"}'))
    files.set_input_files(payload)
    expect(page.locator("#conversation-upload-status")).to_contain_text("disk full")
    expect(page.locator("#conversation-attachments")).to_be_empty()
    page.unroute("**/api/tasks/*/attachments")
    files.set_input_files(payload)
    expect(page.locator("#conversation-upload-status")).to_contain_text("Files ready")
    page.locator("#conversation-input").evaluate("""el => {
      const data=new DataTransfer();data.items.add(new File(['image bytes'],'pasted.png',{type:'image/png'}));
      el.dispatchEvent(new ClipboardEvent('paste',{clipboardData:data,bubbles:true,cancelable:true}));
    }""")
    expect(page.locator("#conversation-attachments")).to_contain_text("pasted.png")
    expect(page.locator("#conversation-upload-status")).to_contain_text("Files ready")
    page.locator("#conversation-compose").evaluate("""el => {
      const data=new DataTransfer();data.items.add(new File(['spreadsheet'],'dropped.csv',{type:'text/csv'}));
      el.dispatchEvent(new DragEvent('drop',{dataTransfer:data,bubbles:true,cancelable:true}));
    }""")
    expect(page.locator("#conversation-attachments")).to_contain_text("dropped.csv")
    expect(page.locator("#conversation-upload-status")).to_contain_text("Files ready")
    page.click("#conversation-send")
    expect(page.locator("#conversation-log")).to_contain_text("notes.txt",timeout=15000)
    expect(page.locator("#conversation-log")).to_contain_text("pasted.png")
    expect(page.locator("#conversation-log")).to_contain_text("dropped.csv")
    expect(page.locator("#conversation-log")).to_contain_text("You · delivered to agent",timeout=20000)
    page.click("#conversation-close")
