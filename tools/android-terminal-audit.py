#!/usr/bin/env python3
"""Native Android/Gboard audit of an explicitly disposable shell.

Requires a booted Pixel 7 AVD (1080x2400, density 420), Chrome, Gboard QWERTY,
and a Python environment with Playwright. No production session is created or
stopped. --allow-input acknowledges that commands will be typed into --session.
ADB reverse must make --url reachable inside the emulator; CDP must be forwarded.
"""
import argparse
import json
import re
import subprocess
import urllib.request
from pathlib import Path
from playwright.sync_api import sync_playwright, expect

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--url', required=True)
parser.add_argument('--session', type=int, required=True)
parser.add_argument('--serial', required=True)
parser.add_argument('--adb', default='adb')
parser.add_argument('--cdp', default='http://127.0.0.1:19222')
parser.add_argument('--artifacts', type=Path, required=True)
parser.add_argument('--allow-input', action='store_true')
args = parser.parse_args()
if not args.allow_input or not args.serial.startswith('emulator-'):
    parser.error('Requires --allow-input and an explicit emulator serial')
base = args.url.rstrip('/')
with urllib.request.urlopen(base + '/api/sessions') as response:
    session = next((s for s in json.load(response) if s['id'] == args.session), None)
if not session or session.get('agent') != 'shell' or session.get('ended_at'):
    parser.error('Session must be a running disposable shell, not an agent')
args.artifacts.mkdir(parents=True, exist_ok=True)
(args.artifacts / 'result.json').unlink(missing_ok=True)  # no stale PASS after a failed run

def adb(*argv):
    return subprocess.check_output([args.adb, '-s', args.serial, *argv], timeout=20)

def screenshot(name):
    (args.artifacts / (name + '.png')).write_bytes(adb('exec-out', 'screencap', '-p'))

def tap(x, y):
    adb('shell', 'input', 'tap', str(x), str(y))

if not re.search(r'Physical size: 1080x2400', adb('shell', 'wm', 'size').decode()):
    parser.error('Native key coordinates require the 1080x2400 Pixel 7 profile')
report = {'serial': args.serial, 'session': args.session, 'checks': []}
with sync_playwright() as p:
    browser = p.chromium.connect_over_cdp(args.cdp)
    page = next((page for page in browser.contexts[0].pages if page.url.startswith(base)), None)
    if page is None:
        parser.error('Open the audit URL in Android Chrome before connecting')
    page.goto(f'{base}/#terminals/session/{args.session}')
    page.reload()  # a hash-only navigation would keep the previous build alive
    frame = page.frame_locator(f'iframe[src="/terminal/session/{args.session}?embed=1"]')
    expect(frame.locator('#connection')).to_have_text('Connected', timeout=20000)
    frame.locator('#agent-terminal').click()
    page.keyboard.press('Control+u')
    page.keyboard.type('echo ')
    # Wait for the actual OS keyboard resize rather than a guessed startup delay.
    page.wait_for_function('innerHeight < 600')
    screenshot('keyboard-before-typing')
    # Physical taps on Gboard, deliberately not page.keyboard.type('hello').
    for x, y in [(645,1865),(270,1710),(970,1865),(970,1865),(917,1710)]:
        tap(x, y)
    expect(frame.locator('.xterm-screen')).to_contain_text('hello')
    screenshot('word-and-prediction')
    tap(540,1575)  # middle prediction: jello on this English Gboard profile
    tap(980,2180)  # native Enter
    page.wait_for_function("() => [...document.querySelector('iframe').contentDocument.querySelectorAll('.xterm-rows > div')].some(row => row.textContent.trim() === 'jello')")
    report['checks'].append('Gboard prediction replaces hello with jello exactly once')
    screen = frame.locator('.xterm-screen').inner_text()
    assert 'hellojello' not in screen and 'jello jello' not in screen, screen
    screenshot('prediction-committed')
    frame.locator('#terminal-keyboard').click()
    page.wait_for_function('innerHeight > 650')
    frame.locator('[data-terminal-key="up"]').click()
    assert page.evaluate('innerHeight') > 650, 'Arrow unexpectedly opened OS keyboard'
    report['checks'].append('Hide keyboard and use arrows without reopening it')
    frame.locator('#terminal-keyboard').click()
    page.wait_for_function('innerHeight < 600')
    report['checks'].append('Show keyboard opens actual OS keyboard')
    adb('shell', 'input', 'keyevent', '4')
    page.wait_for_function('innerHeight > 650')
    expect(frame.locator('#terminal-keyboard')).to_have_attribute('aria-label', 'Show keyboard')
    frame.locator('#terminal-keyboard').click()
    page.wait_for_function('innerHeight < 600')
    report['checks'].append('System Back dismissal permits one-tap keyboard reopen')
    page.keyboard.press('Control+u')
    frame.locator('#terminal-tools-summary').click()
    frame.locator('#compose').click()
    frame.locator('#terminal-draft').fill('echo COMPOSER-$((6*7))')
    screenshot('composer')
    frame.get_by_role('button', name='Insert', exact=True).click()
    assert 'COMPOSER-42' not in frame.locator('.xterm-screen').inner_text()
    frame.locator('#agent-terminal').click()
    page.keyboard.press('Enter')
    expect(frame.locator('.xterm-screen')).to_contain_text('COMPOSER-42')
    report['checks'].append('Composer inserts without executing; Enter executes once')
    # Exercise scrollback and long press through the Android input dispatcher.
    frame.locator('#agent-terminal').click()
    page.keyboard.type("printf 'AUDIT-LINE-%03d\\n' {1..150}")
    page.keyboard.press('Enter')
    expect(frame.locator('.xterm-screen')).to_contain_text('AUDIT-LINE-150')
    frame.locator('#terminal-keyboard').click()
    page.wait_for_function('innerHeight > 650')
    for _ in range(10):
        adb('shell', 'input', 'swipe', '500', '650', '500', '1850', '450')
        if frame.locator('#return-live').count():
            break
    expect(frame.locator('#return-live')).to_be_visible()
    screenshot('retained-history')
    frame.locator('#return-live').click()
    expect(frame.locator('#return-live')).to_have_count(0)
    assert page.evaluate('innerHeight') > 650
    report['checks'].append('Native swipe reaches retained history; Live returns without keyboard')
    adb('shell', 'input', 'swipe', '450', '800', '450', '800', '750')
    expect(frame.locator('#select-bar')).to_be_visible()
    screenshot('text-selection')
    frame.locator('#select-done').click()
    expect(frame.locator('#select-bar')).to_have_count(0)
    report['checks'].append('Native long press opens text selection; Done resumes')
    old_rotation = adb('shell', 'settings', 'get', 'system', 'user_rotation').decode().strip()
    old_auto = adb('shell', 'settings', 'get', 'system', 'accelerometer_rotation').decode().strip()
    try:
        adb('shell', 'settings', 'put', 'system', 'accelerometer_rotation', '0')
        adb('shell', 'settings', 'put', 'system', 'user_rotation', '1')
        page.wait_for_function('innerWidth > innerHeight')
        screenshot('landscape')
        box = frame.locator('#terminal-keybar').bounding_box()
        assert box and box['y'] + box['height'] <= page.evaluate('innerHeight') + 2
        report['checks'].append('Landscape keeps terminal keys within the visible viewport')
    finally:
        adb('shell', 'settings', 'put', 'system', 'user_rotation', old_rotation)
        adb('shell', 'settings', 'put', 'system', 'accelerometer_rotation', old_auto)
    page.wait_for_function('innerWidth < 500')
    report['viewport'] = page.evaluate('({width:innerWidth,height:innerHeight,dpr:devicePixelRatio})')
    screenshot('final')
(args.artifacts / 'result.json').write_text(json.dumps(report, indent=2) + '\n')
print(f'PASS: {len(report["checks"])}/{len(report["checks"])} native Android checks')
