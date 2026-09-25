#!/usr/bin/env python3
"""Real Docker/SSH first-install acceptance, no user keys or agent credentials.

LECTERN_TEST_IMAGE=lectern:dev python3 tools/test-container-install.py
Optional LECTERN_TEST_DOCKER='sudo pct exec 104 -- docker' for a remote daemon.
Optional LECTERN_TEST_URL=http://host:19110 enables desktop/mobile browser checks.
Only uniquely named resources created by this run are removed.
"""
import json
import os
from pathlib import Path
import secrets
import shlex
import subprocess
import tempfile
import time

DOCKER = shlex.split(os.environ.get('LECTERN_TEST_DOCKER', 'docker'))
IMAGE = os.environ.get('LECTERN_TEST_IMAGE', 'lectern:dev')
NAME = 'lectern-first-run-' + secrets.token_hex(4)
SERVER, TARGET, VOL = NAME + '-server', NAME + '-ssh', NAME + '-data'
TOKEN = secrets.token_hex(32)


def docker(*args, input=None, check=True):
    p = subprocess.run(DOCKER + list(args), input=input, capture_output=True,
                       text=True, timeout=240)
    if check and p.returncode:
        raise RuntimeError(f'docker {args[0]} failed: {p.stderr[-2500:]}')
    return p.stdout.strip()


def api(path, data=None, method=None):
    args = ['exec', SERVER, 'curl', '-fsS', '-H', 'Authorization: Bearer ' + TOKEN,
            '-H', 'Content-Type: application/json']
    if method:
        args += ['-X', method]
    if data is not None:
        args += ['-d', json.dumps(data)]
    return json.loads(docker(*args, 'http://localhost:9110/api' + path) or 'null')


def start():
    args = ['run', '-d', '--name', SERVER, '--network', NAME,
            '--memory', '384m', '--cpus', '1', '-v', VOL + ':/data',
            '-e', 'LECTERN_AUTH=token', '-e', 'LECTERN_AUTH_TOKEN=' + TOKEN]
    if os.environ.get('LECTERN_TEST_URL'):
        args += ['-p', '19110:9110']
    docker(*args, IMAGE)
    for _ in range(100):
        try:
            if api('/health')['ok']:
                return
        except RuntimeError:
            pass
        time.sleep(.2)
    raise AssertionError('container did not become healthy')


def file_in(container, path):
    return docker('exec', container, 'cat', path)


def proof(session, container, marker):
    path = session['workdir'] + '/install-proof'
    api(f"/sessions/{session['id']}/send", {'text': f"printf '{marker}' > install-proof"})
    for _ in range(50):
        if docker('exec', container, 'cat', path, check=False) == marker:
            return path
        time.sleep(.2)
    raise AssertionError('terminal input did not reach ' + container)


def browser_checks():
    url = os.environ.get('LECTERN_TEST_URL')
    if not url:
        return
    from playwright.sync_api import sync_playwright, expect
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        for width, height in [(1280, 800), (390, 844)]:
            ctx = browser.new_context(viewport={'width': width, 'height': height})
            page = ctx.new_page()
            page.goto(url)
            dialog = page.get_by_role('dialog', name='Access token', exact=True)
            expect(dialog).to_be_visible()
            dialog.get_by_label('Access token', exact=True).fill(TOKEN)
            dialog.get_by_role('button', name='Connect', exact=True).click()
            expect(dialog).not_to_be_visible()
            page.get_by_role('button', name='Add your first machine').click()
            page.get_by_role('button', name='Add machine', exact=True).click()
            form = page.get_by_role('dialog', name='Add machine', exact=True)
            expect(form).to_be_visible()
            box = form.bounding_box()
            assert box['x'] >= 0 and box['x'] + box['width'] <= width + 1, box
            form.get_by_label('Machine name').fill('browser-' + str(width))
            form.get_by_label('Connection').select_option('local')
            form.get_by_role('button', name='Save machine').click()
            expect(form).not_to_be_visible()
            expect(page.get_by_role('button', name='Start your first session')).to_be_visible()
            target = next(t for t in api('/targets') if t['name'] == 'browser-' + str(width))
            api('/targets/' + str(target['id']), method='DELETE')
            ctx.close()
        browser.close()
    print('PASS Docker token login and first-machine setup at desktop/mobile widths', flush=True)


def browser_terminal(session):
    url = os.environ.get('LECTERN_TEST_URL')
    if not url:
        return
    from playwright.sync_api import sync_playwright, expect
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        for width, height in [(1280, 800), (390, 844)]:
            ctx = browser.new_context(viewport={'width': width, 'height': height})
            ctx.add_init_script("localStorage.setItem('lec-token', " + json.dumps(TOKEN) + ");")
            page = ctx.new_page()
            page.goto(url + '/terminal/session/' + str(session['id']))
            expect(page.locator('#connection')).to_have_text('Connected', timeout=30000)
            page.locator('#agent-terminal').click()
            marker = 'BROWSER_SSH_' + str(width)
            page.keyboard.type("printf '" + marker + "' > browser-proof")
            page.keyboard.press('Enter')
            for _ in range(50):
                if docker('exec', TARGET, 'cat', session['workdir'] + '/browser-proof', check=False) == marker:
                    break
                time.sleep(.2)
            assert file_in(TARGET, session['workdir'] + '/browser-proof') == marker
            ctx.close()
        browser.close()
    print('PASS desktop/mobile browser WebSocket terminal input to SSH target after container recreation', flush=True)


def main():
    docker('network', 'create', NAME)
    docker('volume', 'create', VOL)
    try:
        start()
        status = docker('exec', SERVER, 'curl', '-s', '-o', '/dev/null', '-w', '%{http_code}', 'http://localhost:9110/api/targets')
        assert status == '401', status
        assert api('/targets') == []
        assert api('/onboarding')['python']['ok']
        # Execute the actual configured image health check with token auth on.
        health = json.loads(docker('inspect', SERVER))[0]['Config']['Healthcheck']['Test']
        assert health[0] == 'CMD-SHELL'
        docker('exec', SERVER, 'sh', '-c', health[1])
        browser_checks()
        local = api('/targets', {'name': 'container', 'kind': 'local'})
        shell = api('/shells', {'target_id': local['id']})
        assert shell['workdir'].startswith('/data/'), shell['workdir']
        local_path = proof(shell, SERVER, 'LOCAL_PERSISTED')
        docker('run', '-d', '--name', TARGET, '--network', NAME, '--memory', '384m', '--cpus', '1', IMAGE, 'sleep', 'infinity')
        docker('exec', TARGET, 'sh', '-c', 'apt-get update -qq && apt-get install -y -qq openssh-server >/dev/null && mkdir -p /run/sshd /data/home/.ssh /root/.ssh && chmod 700 /root/.ssh')
        with tempfile.TemporaryDirectory(prefix=NAME) as temp:
            key = Path(temp) / 'key'
            subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(key)], check=True)
            docker('exec', '-i', TARGET, 'sh', '-c', 'cat > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys', input=key.with_suffix('.pub').read_text())
            docker('exec', '-i', SERVER, 'sh', '-c', 'mkdir -p /data/home/.ssh && chmod 700 /data/home/.ssh && cat > /data/home/.ssh/test-key && chmod 600 /data/home/.ssh/test-key', input=key.read_text())
        docker('exec', '-d', TARGET, '/usr/sbin/sshd', '-D', '-e')
        time.sleep(.5)
        remote = api('/targets', {'name': 'remote', 'kind': 'ssh', 'host': TARGET, 'user': 'root', 'key_path': '/data/home/.ssh/test-key'})
        checked = api(f"/targets/{remote['id']}/check", {})
        assert checked['status'] == 'online', checked
        remote_shell = api('/shells', {'target_id': remote['id']})
        remote_path = proof(remote_shell, TARGET, 'REMOTE_BEFORE_RESTART')
        # Recreate the container, not merely its process: this detects missing volumes.
        docker('rm', '-f', SERVER)
        start()
        assert file_in(SERVER, local_path) == 'LOCAL_PERSISTED'
        assert file_in(TARGET, remote_path) == 'REMOTE_BEFORE_RESTART'
        assert len(api('/targets')) == 2
        proof(remote_shell, TARGET, 'REMOTE_AFTER_RESTART')
        browser_terminal(remote_shell)
        print('PASS Docker fresh boot, authenticated health, local shell input/files, real SSH probe/shell and remote reattach after container recreation', flush=True)
    finally:
        docker('rm', '-f', SERVER, TARGET, check=False)
        docker('volume', 'rm', VOL, check=False)
        docker('network', 'rm', NAME, check=False)


if __name__ == '__main__':
    main()
