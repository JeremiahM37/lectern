"""Voice mode: the free, browser-native hands-free loop over a session's
Chat view (SpeechRecognition in, speechSynthesis out — no vendor keys).

A real SpeechRecognition/speechSynthesis are unavailable in a headless
browser, so every test here injects fakes via add_init_script, exactly like
test_mobile_now_strip_and_voice.py already does for dictation's mic button.
The fakes let a test fire a scripted recognition result and inspect what
speechSynthesis was asked to say, so the real hook (useVoiceMode) runs
end to end against a real lectern server.
"""
import json

from playwright.sync_api import expect

from conftest import PHONE


def _inject_fake_speech(page):
    page.add_init_script(
        """
        window.__voice = { synth: [], cancels: 0, starts: 0 };
        function FakeRecognition() {
          this.lang = '';
          this.continuous = false;
          this.interimResults = false;
          this.onresult = null;
          this.onend = null;
          this.onerror = null;
          window.__voice.last = this;
        }
        FakeRecognition.prototype.start = function () {
          window.__voice.starts += 1;
        };
        FakeRecognition.prototype.stop = function () {
          if (this.onend) this.onend();
        };
        window.SpeechRecognition = FakeRecognition;
        window.webkitSpeechRecognition = FakeRecognition;
        function FakeUtterance(text) {
          this.text = text;
          this.rate = 1;
          this.lang = '';
          this.voice = null;
          this.onstart = null;
          this.onend = null;
          this.onerror = null;
        }
        Object.defineProperty(window, 'SpeechSynthesisUtterance', { value: FakeUtterance, configurable: true, writable: true });
        var fakeSynth = {
          speaking: false,
          speak: function (utterance) {
            window.__voice.synth.push(utterance.text);
            window.speechSynthesis.speaking = true;
            if (utterance.onstart) utterance.onstart();
            setTimeout(function () {
              window.speechSynthesis.speaking = false;
              if (utterance.onend) utterance.onend();
            }, 0);
          },
          cancel: function () {
            window.__voice.cancels += 1;
            window.speechSynthesis.speaking = false;
          },
          getVoices: function () { return []; },
          addEventListener: function () {},
          removeEventListener: function () {},
        };
        Object.defineProperty(window, 'speechSynthesis', { value: fakeSynth, configurable: true, writable: true });
        """
    )


def _remove_speech(page):
    page.add_init_script(
        "delete window.SpeechRecognition;delete window.webkitSpeechRecognition;"
        "delete window.speechSynthesis;"
    )


def _fire_result(page, transcript, is_final=True):
    page.evaluate(
        "(t)=>window.__voice.last.onresult({results:[[{transcript:t,isFinal:true}]]})",
        transcript,
    )


def _open_chat(page, server, name):
    page.request.post(
        server + "/api/sessions", data={"name": name, "scratch": True, "agent": "claude"}
    )
    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    page.locator(".scard", has_text=name).get_by_role(
        "button", name="Chat", exact=True
    ).click()
    expect(page.locator("#conversation-input")).to_be_visible(timeout=20000)


# ---- capability handling -----------------------------------------------------


def test_voice_mode_toggle_appears_when_speech_apis_exist(page, server):
    _inject_fake_speech(page)
    _open_chat(page, server, "Voice capable session")
    expect(page.locator("#voice-mode-toggle")).to_be_visible(timeout=20000)


def test_voice_mode_is_hidden_and_explained_without_the_speech_apis(page, server):
    _remove_speech(page)
    _open_chat(page, server, "No speech APIs here")
    expect(page.locator("#voice-mode-toggle")).to_have_count(0)
    expect(page.locator("#voice-mode-unsupported")).to_contain_text("Web Speech API")


# ---- the hands-free loop ------------------------------------------------------


def test_toggling_on_starts_listening_and_off_stops_it(page, server):
    _inject_fake_speech(page)
    _open_chat(page, server, "Toggle voice mode")
    page.locator("#voice-mode-toggle").click()
    expect(page.locator("#voice-mode-panel")).to_be_visible()
    assert page.evaluate("window.__voice.starts") >= 1
    page.locator("#voice-mode-toggle").click()
    expect(page.locator("#voice-mode-panel")).to_have_count(0)


def test_an_utterance_with_the_send_trigger_word_sends_after_the_cancel_window(page, server):
    _inject_fake_speech(page)
    page.request.post(
        server + "/api/sessions",
        data={"name": "Voice send test", "scratch": True, "agent": "claude"},
    )
    sent = {"count": 0, "body": None}

    def record_send(route):
        sent["count"] += 1
        sent["body"] = json.loads(route.request.post_data or "{}")
        route.fulfill(status=200, content_type="application/json", body="{}")

    page.route("**/api/sessions/*/send", record_send)
    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    page.locator(".scard", has_text="Voice send test").get_by_role(
        "button", name="Chat", exact=True
    ).click()
    expect(page.locator("#conversation-input")).to_be_visible(timeout=20000)
    page.locator("#voice-mode-toggle").click()
    expect(page.locator("#voice-mode-panel")).to_be_visible()

    _fire_result(page, "run the deploy script send it")
    expect(page.locator("#voice-mode-pending")).to_contain_text("run the deploy script")

    # Still within the 2s cancel window: nothing has been sent yet.
    assert sent["count"] == 0
    expect(page.locator("#voice-mode-pending")).to_have_count(0, timeout=3000)
    assert sent["count"] == 1
    assert sent["body"]["text"] == "run the deploy script"


def test_tapping_cancel_during_the_send_window_sends_nothing(page, server):
    _inject_fake_speech(page)
    sent = {"count": 0}
    page.route(
        "**/api/sessions/*/send",
        lambda route: (sent.__setitem__("count", sent["count"] + 1), route.fulfill(status=200, content_type="application/json", body="{}"))[1],
    )
    _open_chat(page, server, "Voice cancel test")
    page.locator("#voice-mode-toggle").click()
    _fire_result(page, "run the deploy script send it")
    expect(page.locator("#voice-mode-pending")).to_be_visible()
    page.locator("#voice-mode-cancel-send").click()
    expect(page.locator("#voice-mode-pending")).to_have_count(0)
    page.wait_for_timeout(2200)
    assert sent["count"] == 0


def test_starting_to_talk_barges_in_on_speech(page, server):
    _inject_fake_speech(page)
    _open_chat(page, server, "Voice barge in")
    page.locator("#voice-mode-toggle").click()
    # A long reply queued for read-back leaves speechSynthesis "speaking".
    assert page.evaluate(
        "window.speechSynthesis.speak(new SpeechSynthesisUtterance('a long reply')); window.speechSynthesis.speaking"
    ) is True
    _fire_result(page, "hang on", is_final=False)
    assert page.evaluate("window.__voice.cancels") >= 1


# ---- spoken approvals ----------------------------------------------------------


def _mock_pending_approval(page, session_id, state):
    page.route(
        "**/api/tasks",
        lambda route: route.fulfill(
            status=200,
            content_type="application/json",
            body=json.dumps([{"id": 42, "takeover": {"session_id": session_id}}]),
        )
        if route.request.method == "GET"
        else route.continue_(),
    )
    page.route(
        "**/api/approvals?status=pending",
        lambda route: route.fulfill(
            status=200,
            content_type="application/json",
            # Once the person's spoken decision lands, the real backend drops
            # a decided approval from the pending list — mirror that here, or
            # the mock re-announces the same approval on the next 2s poll.
            body=json.dumps(
                []
                if state["decided"]
                else [
                    {
                        "id": 99,
                        "attempt_id": 1,
                        "task_id": 42,
                        "tool_name": "Bash",
                        "status": "pending",
                        "decided_by": "",
                        "note": "",
                        "created_at": 0,
                        "decided_at": None,
                        "input": {"command": "rm -rf /tmp/scratch"},
                    }
                ]
            ),
        ),
    )


def test_spoken_approve_decides_a_pending_approval(page, server):
    _inject_fake_speech(page)
    created = page.request.post(
        server + "/api/sessions",
        data={"name": "Voice approval test", "scratch": True, "agent": "claude"},
    ).json()
    session_id = created["id"]
    state = {"decided": False}
    _mock_pending_approval(page, session_id, state)
    decided = {"body": None}

    def record_decision(route):
        decided["body"] = json.loads(route.request.post_data or "{}")
        state["decided"] = True
        route.fulfill(status=200, content_type="application/json", body="null")

    page.route("**/api/approvals/99/decision", record_decision)

    page.set_viewport_size(PHONE)
    page.goto(server + "/#sessions")
    page.locator(".scard", has_text="Voice approval test").get_by_role(
        "button", name="Chat", exact=True
    ).click()
    expect(page.locator("#conversation-input")).to_be_visible(timeout=20000)
    page.locator("#voice-mode-toggle").click()

    expect(page.locator("#voice-mode-status")).to_contain_text(
        "Say approve or deny", timeout=20000
    )
    assert any("approve or deny" in line for line in page.evaluate("window.__voice.synth"))

    _fire_result(page, "approve")
    expect(page.locator("#voice-mode-status")).to_contain_text("Approved.", timeout=5000)
    assert decided["body"] == {"decision": "approved"}
    assert "Approved." in page.evaluate("window.__voice.synth")


# ---- layout ---------------------------------------------------------------------


def test_voice_mode_panel_causes_no_horizontal_overflow_at_phone_width(page, server):
    _inject_fake_speech(page)
    _open_chat(page, server, "Voice layout test")
    page.locator("#voice-mode-toggle").click()
    expect(page.locator("#voice-mode-panel")).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth<=innerWidth")
