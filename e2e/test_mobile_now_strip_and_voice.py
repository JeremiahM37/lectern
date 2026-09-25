"""The phone home's glanceable "Now" strip, and the mic button that shows up
wherever the Web Speech API is (and is not) available.

Reuses the same mock-server attention fixtures ("[mock:approval]" tasks, a
scratch session that settles into "waiting") that test_mobile_session_home.py
already established for Needs-you — the Now strip is the same live state,
summarized above it.
"""
from playwright.sync_api import expect

from conftest import PHONE


def test_now_strip_shows_a_waiting_session_and_the_approval_count(page, server):
    project = page.request.get(server + "/api/projects").json()[0]["id"]
    task = page.request.post(
        server + "/api/tasks",
        data={"project_id": project, "title": "Gated deploy", "prompt": "deploy [mock:approval]", "permission_mode": "default"},
    ).json()
    page.request.post(server + "/api/tasks/%d/dispatch" % task["id"], data={})
    page.request.post(
        server + "/api/sessions",
        data={"name": "Waiting agent", "scratch": True, "agent": "claude"},
    )

    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    card = page.locator(".scard", has_text="Waiting agent")
    expect(card.locator(".sstate")).to_contain_text("wants you", timeout=25000)
    # Same settle-then-reload as the equivalent Needs-you test — the mock
    # task's approval lands over SSE, but a reload is the deterministic way
    # to prove the strip reflects a fresh full load, not just a live event.
    page.reload()

    strip = page.locator("#now-strip")
    expect(strip).to_be_visible(timeout=25000)

    approvals_chip = page.locator("#now-approvals")
    expect(approvals_chip).to_contain_text("1 to approve", timeout=25000)

    waiting_chip = strip.locator('.now-chip[data-state="waiting"]')
    expect(waiting_chip).to_contain_text("Waiting agent")
    expect(waiting_chip).to_contain_text("Wants you")

    # The strip sits above the fold and never causes horizontal overflow at
    # phone width, same guarantee every other mobile surface here keeps.
    assert strip.bounding_box()["y"] < page.locator("#sesslist").bounding_box()["y"]
    assert page.evaluate("document.documentElement.scrollWidth<=innerWidth")

    # One tap into the session it names.
    waiting_chip.click()
    expect(page.locator(".scard", has_text="Waiting agent")).to_be_visible()

    # One tap into Needs-you for the approval count.
    approvals_chip.click()
    expect(page.locator("#needs-you")).to_be_in_viewport()


def test_now_strip_stays_out_of_the_way_with_nothing_to_show(page, server):
    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    # A fresh mock server has no sessions and no approvals at all.
    expect(page.locator("#now-strip")).to_have_count(0)


# ---- the mic button follows real Web Speech API feature detection ------------


def _stub_speech_recognition(page):
    page.add_init_script(
        "window.SpeechRecognition = function(){"
        "this.start=function(){};this.stop=function(){};};"
    )


def _remove_speech_recognition(page):
    page.add_init_script(
        "delete window.SpeechRecognition;delete window.webkitSpeechRecognition;"
    )


def test_mic_buttons_appear_when_speech_recognition_exists(page, server):
    _stub_speech_recognition(page)
    page.request.post(
        server + "/api/sessions",
        data={"name": "Dictation session", "scratch": True, "agent": "claude"},
    )
    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    page.locator(".scard", has_text="Dictation session").get_by_role(
        "button", name="Chat", exact=True
    ).click()
    expect(page.locator("#conversation-mic")).to_be_visible(timeout=20000)


def test_mic_buttons_are_hidden_without_speech_recognition(page, server):
    _remove_speech_recognition(page)
    page.request.post(
        server + "/api/sessions",
        data={"name": "No dictation here", "scratch": True, "agent": "claude"},
    )
    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    page.locator(".scard", has_text="No dictation here").get_by_role(
        "button", name="Chat", exact=True
    ).click()
    expect(page.locator("#conversation-input")).to_be_visible(timeout=20000)
    expect(page.locator("#conversation-mic")).to_be_hidden()


def test_deny_with_reason_mic_follows_the_same_feature_detection(page, server):
    _stub_speech_recognition(page)
    project = page.request.get(server + "/api/projects").json()[0]["id"]
    task = page.request.post(
        server + "/api/tasks",
        data={"project_id": project, "title": "Gated deploy", "prompt": "deploy [mock:approval]", "permission_mode": "default"},
    ).json()
    page.request.post(server + "/api/tasks/%d/dispatch" % task["id"], data={})

    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    needs = page.locator("#needs-you")
    expect(needs).to_be_visible(timeout=25000)
    needs.locator('.ny-row[data-reason="approval"]').get_by_role(
        "button", name="Deny with reason…", exact=True
    ).click()
    expect(page.locator("#needs-you-deny-mic")).to_be_visible()
