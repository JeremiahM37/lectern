"""The phone session home: chat first, and what needs a person answered up top.

Two real surfaces back this file.  The mock server fabricates attention states
(a pending approval, a waiting agent, a failed task, work in review) through the
real API.  ``real_terminal`` gives a real tmux session and git worktree, so the
conversation's output, draft, changed files and terminal escape are exercised
against the genuine reader, send and review endpoints.
"""
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE
from test_terminal_workspace import real_terminal  # noqa: F401  (fixture)


def session_card(page, name="Real terminal"):
    return page.locator(".scard", has_text=name)


def font_size(page, selector):
    return float(
        page.locator(selector).evaluate("el=>getComputedStyle(el).fontSize").replace("px", "")
    )


# ---- chat first on a phone, terminal still one tap away ----------------------


def test_phone_session_card_leads_with_chat_and_keeps_terminal_one_tap(page, real_terminal):
    t = real_terminal
    page.set_viewport_size(PHONE)
    page.goto(t["url"] + "/#sessions")
    card = session_card(page)
    chat = card.get_by_role("button", name="Chat", exact=True)
    attach = card.get_by_role("button", name="⌨ Attach", exact=True)
    expect(chat).to_be_visible()
    expect(attach).to_be_visible()
    cb, ab = chat.bounding_box(), attach.bounding_box()
    assert cb["x"] < ab["x"], (cb, ab)
    assert ab["x"] + ab["width"] <= 391, ab
    assert page.evaluate("document.documentElement.scrollWidth<=innerWidth")


def test_desktop_session_card_keeps_the_terminal_first(page, real_terminal):
    t = real_terminal
    page.set_viewport_size(DESKTOP)
    page.goto(t["url"] + "/#sessions")
    card = session_card(page)
    cb = card.get_by_role("button", name="Chat", exact=True).bounding_box()
    ab = card.get_by_role("button", name="⌨ Attach", exact=True).bounding_box()
    assert ab["x"] < cb["x"], (cb, ab)


# ---- the conversation brings the reader, the draft and the changes together --


def test_session_chat_brings_output_draft_and_changed_files(page, real_terminal):
    t = real_terminal
    (t["root"] / "agent-edit.txt").write_text("changed by the agent\n")
    page.set_viewport_size(PHONE)
    page.goto(t["url"] + "/#sessions")
    session_card(page).get_by_role("button", name="Chat", exact=True).click()

    expect(page.locator("#conversation-log")).to_contain_text("$", timeout=20000)
    expect(page.locator("#conversation-status")).to_have_attribute(
        "data-connection", "live", timeout=20000
    )
    assert font_size(page, "#conversation-input") >= 16

    page.locator("#conversation-input").fill("Keep this draft until I am back")
    expect(page.locator("#conversation-draft")).to_contain_text("not sent")
    page.locator("#conversation-close").click()
    session_card(page).get_by_role("button", name="Chat", exact=True).click()
    expect(page.locator("#conversation-input")).to_have_value("Keep this draft until I am back")
    expect(page.locator("#conversation-draft")).to_contain_text("not sent")

    page.locator("#conversation-changes > summary").click()
    expect(page.locator("#conversation-changes")).to_contain_text("agent-edit.txt", timeout=20000)
    assert page.locator("#conversation").evaluate("e=>e.scrollWidth<=e.clientWidth+1")

    page.locator("#conversation-send").click()
    expect(page.locator("#conversation-receipt")).to_contain_text("Sent to the session", timeout=20000)
    expect(page.locator("#conversation-draft")).to_have_count(0)


def test_session_chat_explains_unreadable_output_and_opens_the_terminal(page, real_terminal):
    t = real_terminal
    page.set_viewport_size(PHONE)
    page.route("**/api/sessions/*/reader", lambda route: route.abort())
    page.goto(t["url"] + "/#sessions")
    session_card(page).get_by_role("button", name="Chat", exact=True).click()
    expect(page.locator("#conversation-output-error")).to_contain_text(
        "could not be read", timeout=20000
    )
    terminal = page.locator("#conversation-terminal")
    expect(terminal).to_be_visible()
    terminal.click()
    expect(page.locator("#conversation")).to_have_count(0)
    frame = page.frame_locator('iframe[title$=" terminal"]')
    expect(frame.locator("#agent-terminal .xterm-screen")).to_contain_text("$", timeout=25000)


def test_session_chat_keeps_the_terminal_one_tap_away_while_the_reader_works(page, real_terminal):
    t = real_terminal
    page.set_viewport_size(PHONE)
    page.goto(t["url"] + "/#sessions")
    session_card(page).get_by_role("button", name="Chat", exact=True).click()
    expect(page.locator("#conversation-log")).to_contain_text("$", timeout=20000)
    expect(page.locator("#conversation-status")).to_have_attribute(
        "data-connection", "live", timeout=20000
    )
    terminal = page.locator("#conversation-terminal")
    expect(terminal).to_be_visible()
    box = terminal.bounding_box()
    assert box["x"] >= 0 and box["x"] + box["width"] <= 391, box
    terminal.click()
    expect(page.locator("#conversation")).to_have_count(0)
    frame = page.frame_locator('iframe[title$=" terminal"]')
    expect(frame.locator("#agent-terminal .xterm-screen")).to_contain_text("$", timeout=25000)


def test_a_narrow_phone_keeps_a_readable_title_and_a_wrapping_terminal_row(page, server):
    name = "Release engineering standup"
    page.request.post(
        server + "/api/sessions", data={"name": name, "scratch": True, "agent": "claude"}
    )
    page.set_viewport_size({"width": 320, "height": 640})
    page.goto(server + "/#sessions")
    session_card(page, name).get_by_role("button", name="Chat", exact=True).click()
    expect(page.locator("#conversation-status")).to_have_attribute(
        "data-connection", "live", timeout=25000
    )
    title = page.locator("#conversation-title")
    expect(title).to_be_visible()
    assert title.bounding_box()["width"] >= 140, title.bounding_box()
    assert font_size(page, "#conversation-title") >= 14
    assert title.evaluate("el=>el.scrollWidth<=el.clientWidth+1")
    terminal = page.locator("#conversation-terminal")
    expect(terminal).to_be_visible()
    box = terminal.bounding_box()
    assert box["x"] >= 0 and box["x"] + box["width"] <= 321, box
    expect(page.locator("#reader-latest")).to_be_visible()
    assert page.locator("#conversation").evaluate("e=>e.scrollWidth<=e.clientWidth+1")
    assert page.evaluate("document.documentElement.scrollWidth<=innerWidth")


def test_a_shell_session_is_not_forced_into_chat(page, server):
    page.request.post(server + "/api/shells", data={})
    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    card = page.locator(".scard", has_text="Shell ·")
    expect(card).to_be_visible(timeout=20000)
    expect(card.get_by_role("button", name="Chat", exact=True)).to_have_count(0)
    expect(card.get_by_role("button", name="⌨ Attach", exact=True)).to_be_visible()


def _button_fit(button):
    """How a row button's own text sits inside its box."""
    return button.evaluate(
        """el => {
          const style = getComputedStyle(el);
          const rect = el.getBoundingClientRect();
          const range = document.createRange();
          range.selectNodeContents(el);
          const text = range.getBoundingClientRect();
          const inner =
            rect.width -
            parseFloat(style.paddingLeft) - parseFloat(style.paddingRight) -
            parseFloat(style.borderLeftWidth) - parseFloat(style.borderRightWidth);
          return {label: el.textContent.trim(), width: rect.width, height: rect.height,
                  x: rect.x, inner: inner, text: text.width};
        }"""
    )


def _assert_row_labels_fit(card, width):
    """Every visible action keeps its whole label, its 40px target, on screen."""
    buttons = card.locator(".btnrow button.b:visible")
    assert buttons.count() >= 3, "the busiest card row should show three or more actions"
    labels = set()
    for index in range(buttons.count()):
        fit = _button_fit(buttons.nth(index))
        labels.add(fit["label"])
        assert fit["text"] <= fit["inner"] + 1, (width, fit)
        assert fit["height"] >= (40 if width <= 600 else 38), (width, fit)
        assert 0 <= fit["x"] and fit["x"] + fit["width"] <= width + 1, (width, fit)
    return labels


def test_phone_card_actions_wrap_instead_of_clipping_their_labels(page, server):
    page.request.post(
        server + "/api/sessions",
        data={"name": "Row of agent actions", "scratch": True, "agent": "claude"},
    )
    page.request.post(server + "/api/shells", data={})
    for width in (320, 390, 1440):
        page.set_viewport_size({"width": width, "height": 844})
        page.goto(server + "/#sessions")
        agent = page.locator(".scard", has_text="Row of agent actions")
        scratch = page.locator(".scard", has_text="Shell ·")
        expect(agent).to_be_visible(timeout=20000)
        expect(scratch).to_be_visible(timeout=20000)
        # A live agent card: Attach, Chat and Switch share one row.
        assert {"⌨ Attach", "Chat", "⇄ Switch"} <= _assert_row_labels_fit(agent, width)
        # A blank scratch shell adds the widest label a phone has to hold.
        assert {"⌨ Attach", "⇑ Make a project", "✎ Rename"} <= _assert_row_labels_fit(
            scratch, width
        )
        assert page.evaluate("document.documentElement.scrollWidth<=innerWidth")


# ---- Needs you answers first -------------------------------------------------


def _task(page, server, title, prompt, perm="default", wait=None):
    project = page.request.get(server + "/api/projects").json()[0]["id"]
    task = page.request.post(
        server + "/api/tasks",
        data={"project_id": project, "title": title, "prompt": prompt, "permission_mode": perm},
    ).json()
    page.request.post(server + "/api/tasks/%d/dispatch" % task["id"], data={})
    if wait:
        for _ in range(200):
            row = page.request.get(server + "/api/tasks/%d" % task["id"]).json()
            if row["status"] == wait:
                break
            page.wait_for_timeout(100)
        assert row["status"] == wait, row
    return task


def test_needs_you_answers_first_on_a_phone(page, server):
    _task(page, server, "Gated deploy", "deploy [mock:approval]")
    _task(page, server, "Crashed run", "fail [mock:fail]", wait="failed")
    _task(page, server, "Ready work", "add a health endpoint", wait="review")
    session = page.request.post(
        server + "/api/sessions", data={"name": "Waiting agent", "scratch": True, "agent": "claude"}
    ).json()
    assert session["id"]

    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    card = page.locator(".scard", has_text="Waiting agent")
    expect(card.locator(".sstate")).to_contain_text("wants you", timeout=25000)
    page.reload()

    needs = page.locator("#needs-you")
    expect(needs).to_be_visible(timeout=25000)
    assert needs.bounding_box()["y"] < page.locator("#sesslist").bounding_box()["y"]

    approval = needs.locator('.ny-row[data-reason="approval"]')
    expect(approval).to_contain_text("Approval needed")
    expect(approval).to_contain_text("Gated deploy")
    expect(needs.locator('.ny-row[data-reason="waiting"]')).to_contain_text("Waiting agent")
    expect(needs.locator('.ny-row[data-reason="waiting"]')).to_contain_text("Wants you")
    expect(needs.locator('.ny-row[data-reason="failed-task"]')).to_contain_text("Crashed run")
    expect(needs.locator('.ny-row[data-reason="review-task"]')).to_contain_text("Ready work")
    expect(needs.locator('.ny-row[data-reason="review-task"]').get_by_role("button", name="Review", exact=True)).to_be_enabled()

    approval.get_by_role("button", name="Approve", exact=True).click()
    expect(needs.locator('.ny-row[data-reason="approval"]')).to_have_count(0, timeout=20000)

    needs.locator('.ny-row[data-reason="waiting"]').get_by_role(
        "button", name="Chat", exact=True
    ).click()
    expect(page.locator("#conversation")).to_be_visible()
    page.locator("#conversation-close").click()

    heights = [
        needs.get_by_role("button").nth(i).bounding_box()["height"]
        for i in range(needs.get_by_role("button").count())
    ]
    assert heights and min(heights) >= 36, heights
    assert page.evaluate("document.documentElement.scrollWidth<=innerWidth")
    needs.locator('.ny-row[data-reason="review-task"]', has_text="Ready work").get_by_role("button", name="Review", exact=True).click()
    expect(page.locator("#sheet .statpill")).to_have_text("review")


# ---- a draft survives being offline, and nothing is submitted ----------------


def test_offline_chat_keeps_the_draft_and_sends_nothing(page, server):
    page.request.post(
        server + "/api/sessions", data={"name": "Offline chat", "scratch": True, "agent": "claude"}
    )
    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    session_card(page, "Offline chat").get_by_role("button", name="Chat", exact=True).click()
    draft = page.locator("#conversation-input")
    draft.fill("Written on a plane")

    sent = []
    page.on(
        "request",
        lambda request: sent.append(request.url)
        if request.method == "POST" and request.url.endswith("/send")
        else None,
    )
    page.context.set_offline(True)
    expect(page.locator("#conversation-status")).to_have_attribute(
        "data-connection", "offline", timeout=25000
    )
    expect(page.locator("#conversation-send")).to_be_disabled()
    expect(page.locator("#conversation-offline")).to_contain_text("nothing is sent")
    expect(page.locator("#conversation-draft")).to_contain_text("not sent")
    assert not sent, sent

    page.context.set_offline(False)
    expect(page.locator("#conversation-status")).to_have_attribute(
        "data-connection", "live", timeout=25000
    )
    expect(draft).to_have_value("Written on a plane")
    with page.expect_request(
        lambda request: request.method == "POST" and request.url.endswith("/send")
    ) as request:
        page.locator("#conversation-send").click()
    assert "Written on a plane" in (request.value.post_data or "")
    assert len(sent) == 1, sent


# ---- the attention list stays honest when it cannot be refreshed -------------


def _poll_needs_you(page):
    # NeedsYou re-reads its sources on visibilitychange; dispatch it rather
    # than waiting out the fifteen-second timer.
    page.evaluate("document.dispatchEvent(new Event('visibilitychange'))")


def test_needs_you_keeps_the_last_rows_and_says_when_it_cannot_refresh(page, server):
    _task(page, server, "Gated deploy", "deploy [mock:approval]")
    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    needs = page.locator("#needs-you")
    row = needs.locator('.ny-row[data-reason="approval"]')
    expect(row).to_contain_text("Gated deploy", timeout=25000)
    expect(page.locator("#needs-you-stale")).to_have_count(0)

    page.route("**/api/approvals*", lambda route: route.abort())
    page.route("**/api/tasks*", lambda route: route.abort())
    _poll_needs_you(page)

    stale = page.locator("#needs-you-stale")
    expect(stale).to_be_visible(timeout=20000)
    expect(stale).to_contain_text("last known state")
    assert needs.get_attribute("data-stale") == "true"
    # The known row is kept, not dropped — an unreachable poll must not read as
    # an empty all-clear.
    expect(row).to_contain_text("Gated deploy")
    assert row.get_attribute("data-stale") == "true"

    page.unroute("**/api/approvals*")
    page.unroute("**/api/tasks*")
    _poll_needs_you(page)
    expect(stale).to_have_count(0, timeout=20000)
    assert needs.get_attribute("data-stale") is None


def test_needs_you_caps_a_long_list_behind_a_toggle(page, server):
    # Waiting sessions, not gated tasks: a mock target runs at most four agents
    # at once, so nine tasks would sit queued instead of asking for a person.
    for i in range(9):
        page.request.post(
            server + "/api/sessions",
            data={"name": "Waiting %d" % i, "scratch": True, "agent": "claude"},
        )
    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    needs = page.locator("#needs-you")
    toggle = page.locator("#needs-you-toggle")
    rows = needs.locator(".ny-row")
    expect(toggle).to_contain_text("Show all 9", timeout=30000)
    expect(rows).to_have_count(8)
    toggle.click()
    expect(rows).to_have_count(9)
    expect(toggle).to_contain_text("Show fewer")
    assert toggle.get_attribute("aria-expanded") == "true"
    toggle.click()
    expect(rows).to_have_count(8)


def test_named_session_can_be_renamed_without_replacing_its_terminal(page, real_terminal):
    import subprocess
    t = real_terminal
    def identity():
        return subprocess.check_output(['tmux', 'display-message', '-p', '-t', '=terminal-test:', '#{pane_pid} #{pane_current_path}'], env=t['env'], text=True)
    before = identity()
    for width in [320, 1440]:
        page.set_viewport_size({'width': width, 'height': 844})
        page.goto(t['url'] + '/#sessions')
        card = page.locator(f'.scard[data-session-id="{t["id"]}"]')
        name = f'Renamed existing session {width}'
        page.once('dialog', lambda dialog: dialog.accept(name))
        card.get_by_role('button', name='✎ Rename', exact=True).click()
        expect(card.locator('.nm')).to_have_text(name)
        page.reload()
        expect(card.locator('.nm')).to_have_text(name)
        assert identity() == before
        assert next(row for row in t['api']('/sessions') if row['id'] == t['id'])['tmux_session'] == 'terminal-test'
