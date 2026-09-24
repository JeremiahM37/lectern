# Mobile terminal audit — 2026-09-22

The terminal needs device-level checks in addition to browser layout tests. A
resized desktop browser never runs Gboard or the operating system's keyboard
show/hide logic. Synthetic composition events can replay a discovered regression,
but cannot discover missing native events.

## Open-source tools considered

| Project | What it provides | Fit for this project |
| --- | --- | --- |
| [Mobile Next / mobile-mcp](https://github.com/mobile-next/mobile-mcp) | MCP actions for Android/iOS devices: accessibility, screenshots, taps, swipes, orientation | Best option for ongoing AI-led exploratory device testing. It controls an emulator/device; it isn't an OS simulator itself. |
| [Maestro](https://github.com/mobile-dev-inc/Maestro) | Declarative UI flows for mobile and web | Useful for repeatable cross-screen journeys. Native keyboard checks still need an actual device/emulator. |
| [Appium](https://appium.io/) | Extensible automation with native and mobile browser drivers | Broad device coverage, more driver/setup machinery than needed for this browser app. |
| [AndroidWorld](https://github.com/google-research/android_world) | Emulator environment and outcome-graded tasks for autonomous agents | Useful benchmark design; its predefined app tasks aren't a Lectern UX test suite. |
| [scrcpy](https://github.com/Genymobile/scrcpy) | Android display/control over ADB | Useful human-visible device control/recording; not itself an AI test agent. |
| [Playwright Android](https://playwright.dev/docs/api/class-android) | Experimental Android Chrome/WebView control | Existing Playwright assertions can inspect the real browser while ADB generates genuine OS touch/keyboard input. |

Selected for this audit: the existing Pixel 7 AVD, Android 14, real Android Chrome,
Gboard, ADB physical taps/swipes and Playwright attached over CDP. No cloud account,
new VM, or changes to the operator's phone are required. Mobile Next remains the
recommended MCP front end when configuring a fresh agent session; it was researched,
not installed globally by this change. iOS Simulator requires an Apple/Xcode host;
desktop WebKit alone does not prove iPhone keyboard behavior.

## UX references

[Termius's mobile guide](https://docs.termius.com/terminal/mobile-terminal) describes
keyboard visibility controls, extra terminal keys, adjustable key groups, touch
navigation, selection and pinch resizing. The applicable principle here is to keep
frequent controls within reach and make typing and reading separate, predictable
activities. We retained existing pinch, selection, sticky modifiers and snippets.
We did not copy Termius's gesture assignments: Lectern already uses horizontal
swipes for session switching and long press for selection.

## Findings and changes

* **Reproduced native Gboard corruption:** type `hello`, tap the `jello` prediction.
  Live baseline produced `hellojello jello`. The candidate emits one replacement,
  `jello`. Native Gboard's non-composing path moves the helper caret to zero without
  a deletion input event, then inserts the replacement. Android now owns the helper's
  edit events rather than having xterm's key-229 and composition paths both send them.
  External keys/paste reset its current-word context. Desktop/iOS use xterm unchanged.
* **Frequent keys off-screen:** the snippets shortcut and Alt consumed the leading
  positions while Right was clipped. Esc, Tab, Ctrl and all four arrows now fit at
  the tested 390px phone width. Extras remain scrollable; Shift-Tab is included.
* **No keyboard control:** an explicit toggle is pinned beside Tools. Extra keys
  preserve the current keyboard state rather than reopening it on every press.
* **Long prompts were awkward to edit:** Tools → Write or paste text opens a regular
  textarea, retains an unsent draft while closed, and separates Insert from Send.
  Insert uses terminal bracketed paste where supported; Send additionally sends Enter.
  Drafts are in-memory only and are not persisted to browser storage.
* **Returning from older output was hidden in Tools:** retained history now has a
  visible Live button, which does not summon the keyboard.
* Terminal dialog inputs now retain 16px text on phones to avoid Safari focus zoom.
* The full browser suite exposed a Tools opening race on desktop: native details
  became visible before a deferred React effect moved the menu out of the toolbar's
  clipping region. Placement now happens during activation; a same-turn hit-test
  reproduced the old failure and guards the first visible frame.

Android baseline keyboard resizing and vertical scrollback already worked. They
were exercised rather than rewritten based on assumptions. Existing terminal
screen font sizes and nonterminal screen layouts are preserved.

## Repeating the checks

`e2e/test_mobile_terminal.py` uses a real isolated tmux/ttyd terminal. It checks
key accessibility, keyboard focus, draft insertion/execution, native Android event
replay and composing input, alongside the older selection/snippet checks.
Run it through `tools/run-isolated-tests.sh`, never against a live tmux socket.

Device exploration must use a disposable **shell**, never a running agent's input.
The helper `tools/android-terminal-audit.py` requires an explicit session and input
opt-in. It records device screenshots and metrics; its native Gboard key coordinates
are intentionally limited to the documented 1080×2400 Pixel 7 profile. Inspect its
pre-typing screenshot if the keyboard version/layout changes. It cannot claim iOS,
other keyboard engines, or all language layouts have been tested.

Native audit environment: Android 14, Chrome 148.0.7778.215, Gboard
17.5.8.917159154, Pixel 7 at 1080×2400 / density 420 (411 CSS pixels). The eight
native checks passed on the candidate: prediction replacement, keyboard hide/show (including system Back),
arrow use while hidden, draft insertion, retained scrollback/Live, long-press
selection, and landscape key visibility. A separate native standalone-terminal
check caught and fixed the footer pushing the keyboard toggle below its key row.

Example setup after starting an emulator and creating a disposable Lectern shell:

```sh
adb -s emulator-5554 reverse tcp:9110 tcp:9110
adb -s emulator-5554 forward tcp:19222 localabstract:chrome_devtools_remote
# Open http://127.0.0.1:9110 in that emulator's Chrome, then:
python tools/android-terminal-audit.py --url http://127.0.0.1:9110 \
  --session YOUR_DISPOSABLE_SHELL_ID --serial emulator-5554 \
  --artifacts .verify-artifacts/android --allow-input
```

The emulator audit uses native Gboard taps for the word and suggestion, rather
than CDP typing disguised as keyboard testing. CDP is used for page inspection
and setup commands. Screenshots include device chrome and the actual keyboard;
the final `result.json` names each completed check. Keep the screenshot artifacts
private if testing an instance with personal sessions.

## Automated nightly and pre-release audit

`tools/run-android-audit.py` owns setup and cleanup. It boots the named AVD only
when the requested emulator is absent, runs a disposable Lectern server and two
synthetic sessions, and stops only an emulator it started. It never connects to
the production Lectern API. A lock serializes scheduled and manual runs.

After reviewing `tools/run-isolated-tests.sh`, run from the candidate checkout:

```sh
ADK_ISOLATION_REVIEWED=1 PATH=/usr/local/go/bin:$PATH \
  python3 tools/run-android-audit.py --sdk "$ANDROID_SDK_ROOT" \
  --avd pixel7_test --serial emulator-5554 \
  --artifacts .verify-artifacts/android
```

The AVD must already contain Chrome and English Gboard with the documented
Pixel 7 geometry. `--preflight` validates local paths without launching or typing.
The audited source must have its frontend built/staged first. Native tests add a
bottom-pinned alternate-screen input fixture, mouse-reporting terminal swipes,
and offline/background recovery to the keyboard/selection checks. This is a
synthetic full-screen application; it does not assert parity with every release
of Claude or Codex, and it does not establish iPhone coverage.

The ordinary test modes retain network isolation. The explicit `android` mode
shares loopback solely to reach the external ADB server and expose the temporary
fixture to the emulator through `adb reverse`. It retains private HOME, /tmp,
mount, UID and PID namespaces. The host's tmux socket and credentials are absent;
fixture processes cannot reach production sessions through a shared tmux socket.
Only the chosen artifact directory is mounted writable outside the private copy.

Every run writes a timestamped directory with `audit.log`, `fixture.log`, device
screenshots, and `result.json`. `latest.json` is replaced on success **or failure**;
a failed run cannot leave yesterday's PASS as today's result. Nonzero exit status
fails the service or release check. Receipts include the tested source revision.

The host service/timer templates are in `deploy/android-audit/`. Install the
runner as `~/.local/bin/lectern-android-audit`, install the units under
`/etc/systemd/system/`, and create `~/.config/lectern/android-audit.env`:

```ini
LEC_AUDIT_CHECKOUT=/absolute/path/to/deployed-source-snapshot
ANDROID_SDK_ROOT=/absolute/path/to/android-sdk
LEC_ANDROID_ARTIFACT_ROOT=/absolute/path/to/private-audit-results
PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin
```

Then `sudo systemctl daemon-reload` and
`sudo systemctl enable --now lectern-android-audit.timer`. The timer runs at
04:30 with up to ten minutes of jitter. Point it at the deployed source snapshot,
not a mutable development checkout. Run the same command against a candidate
before deployment; passing does not publish anything automatically. Check status
with `systemctl status lectern-android-audit.service` and the latest JSON
receipt. An unavailable emulator, failed setup, or failed assertion is a failure,
not a skipped green check.

The example service runs as `admin` (UID 1000) and starts that user's scope bus
without enabling lingering. Adapt the account and `user@` dependency when
installing on another machine. No live server or agent is used by this audit.

## Full-screen follow-up

The new audit adds a bottom-pinned alternate-screen prompt with mouse reporting,
and exercises `interactive-widget=resizes-visual` as well as ordinary Android
layout resizing. Inspect the ADB screenshot: a CDP screenshot can temporarily
resize the visual viewport itself and produce misleading keyboard evidence.
The prompt, key row, keyboard toggle and Tools must all fit above Gboard.
Horizontal swipes are recognized by the terminal's existing gesture owner,
which locks one axis so vertical reading cannot turn into a session change.


## Optional real Claude input check

To exercise the installed Claude renderer in the same isolated test environment:

```sh
ADK_ISOLATION_REVIEWED=1 ADK_TEST_MODE=e2e \
  ADK_TEST_CLAUDE_BIN="$(command -v claude)" \
  ADK_TEST_E2E_ARGS='-q e2e/test_claude_keyboard_probe.py' \
  tools/run-isolated-tests.sh .
```

Only that executable is mounted read-only. The test uses a private config,
a dummy API key and an unreachable loopback API endpoint; it types a long draft
without submitting it. It checks the rendered last input line after visual-viewport
and overlay-keyboard changes. Without the explicit executable mount these two
checks skip. This supplements the native Android audit; it does not establish
coverage of an unknown physical phone/browser combination.

The overlay-keyboard case also exercises `navigator.virtualKeyboard` with both
viewports unchanged. Android Chromium before M152 exposes incorrect rectangle
coordinates ([upstream fix](https://chromium-review.googlesource.com/c/chromium/src/+/7686010));
Lectern uses the reported height for its full-width docked keyboard. Newer
Chromium and floating keyboards retain the rectangle coordinates. Normal
viewport-resizing keyboards do not use this overlay workaround.

Set the same `ADK_TEST_CLAUDE_BIN` when invoking `tools/run-android-audit.py`
to add two native Gboard checks using the real Claude renderer (ordinary resize
and keyboard overlay). They create a disposable third terminal with a private
config and dummy API key, leave the draft unsent, and capture ADB screenshots.
The standard nightly run remains independent of an installed Claude binary.

## Diagnosing intermittent device failures

The audit receipt records the browser user agent, browser exceptions and up to
200 input events from its disposable shell fixture. Failure artifacts include
an Android logcat tail and an ADB screenshot. The input trace contains only the
fixture's synthetic text; the audit never attaches to a live user terminal.

On a cold emulator boot, Chrome can exit before its first page is available.
The fixture retries startup once only when `pidof com.android.chrome` confirms
that the process is absent, preserving the first exit's logcat and both launch
outputs. A browser that is still running is not restarted on a CDP timeout.
Input and layout assertion failures are never retried within an audit run.
