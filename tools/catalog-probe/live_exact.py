"""Live check: exact resume/fork/restore for catalog agents with real CLIs.

Runs a throwaway Lectern (real tmux in a private socket dir) with HOME set to
the probe home ($LECTERN_CLI_PROBE/home) where each CLI is installed and
configured against fake_openai.py on 127.0.0.1:18765. Nothing logs in and no
real provider is called. Usage:

    python3 fake_openai.py 18765 $LECTERN_CLI_PROBE/fake/requests.log &
    python3 live_exact.py /path/to/lectern [agent ...]
 For each agent: start a session with an opening prompt,
wait for the CLI's reply, wait for Lectern to bind the conversation id, stop
it, Restore it (reopen), and check the successor resumed that exact id; then
fork it where the agent supports forking, and run `lectern restore` for one.
"""
import json, os, shutil, subprocess, sys, tempfile, time, urllib.request, urllib.error

LECTERN = sys.argv[1]
ONLY = sys.argv[2:]  # optional agent names
PROBE = os.environ.get('LECTERN_CLI_PROBE', '/mnt/bulk/cli-probe')
HOME = PROBE + '/home'
PORT = 9199
URL = f'http://127.0.0.1:{PORT}'
FAKE = 'http://127.0.0.1:18765/v1'
PATH = ':'.join([PROBE + '/npm/bin', PROBE + '/node/bin', PROBE + '/bin', HOME + '/.local/bin',
                 HOME + '/.mimocode/bin', HOME + '/.grok/bin', PROBE + '/bun/bin', '/usr/local/bin', '/usr/bin', '/bin'])

tmp = tempfile.mkdtemp(prefix='lec-live-', dir='/mnt/bulk/cli-probe')
tmux_dir = os.path.join(tmp, 'tmux'); os.makedirs(tmux_dir)
env = dict(os.environ, HOME=HOME, PATH=PATH, LECTERN_TICK='0.5', LECTERN_PORT=str(PORT),
           LECTERN_DB=os.path.join(tmp, 'l.db'), LECTERN_BASE_URL=URL, TMUX='', TMUX_TMPDIR=tmux_dir,
           XDG_CONFIG_HOME=HOME + '/.config', XDG_DATA_HOME=HOME + '/.local/share', XDG_STATE_HOME=HOME + '/.local/state',
           XDG_CACHE_HOME=HOME + '/.cache', NO_COLOR='', CI='')
for k in ('LECTERN_GRIMOIRE_URL', 'LECTERN_GRIMOIRE_TOKEN', 'LECTERN_API', 'LECTERN_SESSION_ID', 'GRIMOIRE_SESSION', 'LECTERN_CHECKPOINT'):
    env[k] = ''
log = open(os.path.join(tmp, 'server.log'), 'wb')
proc = subprocess.Popen([LECTERN], env=env, stdout=log, stderr=subprocess.STDOUT, cwd=tmp)


def api(path, body=None, method=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(URL + '/api' + path, data=data, method=method or ('POST' if data else 'GET'),
                                 headers={'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            raw = r.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raise RuntimeError(f'{method or "POST"} {path}: {e.code} {e.read().decode()[:400]}')


def pane(name):
    return subprocess.run(['tmux', 'capture-pane', '-p', '-J', '-S', '-2000', '-t', '=' + name + ':'],
                          env=env, capture_output=True, text=True).stdout


def pane_cmd(name):
    """The full command line tmux was started with (the agent invocation)."""
    return subprocess.run(['tmux', 'list-panes', '-t', '=' + name + ':', '-F', '#{pane_start_command}'],
                          env=env, capture_output=True, text=True).stdout


def bound_id(sid):
    try:
        cur = api(f"/sessions/{sid}/conversations").get('current') or {}
    except RuntimeError:
        return None
    return cur.get('id') if cur.get('state') == 'identified' else None


def wait(pred, timeout, what):
    end = time.time() + timeout
    while time.time() < end:
        v = pred()
        if v:
            return v
        time.sleep(1)
    raise RuntimeError('timed out waiting for ' + what)


for _ in range(100):
    try:
        api('/health'); break
    except Exception:
        time.sleep(0.2)
target = api('/targets', {'name': 'probe-local', 'kind': 'local'})
catalog = {p['name']: p for p in api('/agents/catalog')}

OC = {'provider': {'fake': {'npm': '@ai-sdk/openai-compatible', 'name': 'Fake',
      'options': {'baseURL': FAKE, 'apiKey': 'x'}, 'models': {'m1': {'name': 'm1'}}}}, 'model': 'fake/m1'}
setup = {
    'opencode': dict(env={'OPENCODE_CONFIG_CONTENT': json.dumps(OC)}, model='fake/m1'),
    'kilo': dict(env={'KILO_CONFIG_CONTENT': json.dumps(OC)}, model='fake/m1'),
    'mimo': dict(env={'MIMOCODE_CONFIG_CONTENT': json.dumps(OC)}, model='fake/m1', yolo=False),
    'qwen': dict(args=['--auth-type', 'openai', '--openai-base-url', FAKE, '--openai-api-key', 'x'], model='m1'),
    'pi': dict(model='fake/m1'),
    'omp': dict(model='fake/m1'),
    'goose': dict(env={'GOOSE_PROVIDER': 'openai', 'OPENAI_HOST': FAKE[:-3], 'OPENAI_API_KEY': 'x'}, model='m1'),
    'openclaude': dict(args=['--provider', 'openai'], env={'OPENAI_BASE_URL': FAKE, 'OPENAI_API_KEY': 'x'}, model='m1', yolo=False),
    'crush': dict(),
    'hermes': dict(),
    'vibe': dict(env={'FAKE_KEY': 'x'}),
    'cline': dict(),
}
agents = []
for name, extra in setup.items():
    spec = {k: v for k, v in catalog[name].items() if k in (
        'name', 'command', 'args', 'model_flag', 'prompt_arg', 'prompt_args', 'resume_args', 'resume_id_args', 'fork_args',
        'yolo_args', 'yolo_env', 'env', 'trust_command', 'session_id_args', 'fork_session_id', 'sessions', 'acp', 'task')}
    spec['args'] = list(spec.get('args') or []) + extra.get('args', [])
    spec['env'] = dict(spec.get('env') or {}, **extra.get('env', {}))
    agents.append(spec)
api('/agents', agents, 'PUT')

results = {}
names = ONLY or list(setup)
for name in names:
    extra = setup[name]
    ws = os.path.join(tmp, 'ws-' + name); os.makedirs(ws)
    subprocess.run(['git', 'init', '-q', ws])
    r = results[name] = {}
    try:
        s = api('/sessions', {'target_id': target['id'], 'workdir': ws, 'agent': name, 'model': extra.get('model', ''),
                              'name': name + ' live', 'prime': 'say hi', 'yolo': extra.get('yolo', True)})
        s = wait(lambda: (lambda x: x if x.get('tmux_session') else None)(api(f"/sessions/{s['id']}")), 30, 'tmux name')
        time.sleep(3)
        r.pop('early_ls', None); _ = subprocess.run(['tmux', 'ls'], env=env, capture_output=True, text=True).stdout + subprocess.run(['tmux', 'ls'], env=env, capture_output=True, text=True).stderr
        _ = {k: api(f"/sessions/{s['id']}").get(k) for k in ('status', 'ended_at', 'end_reason', 'setup_error', 'pane_tail')}
        r['tmux'] = s['tmux_session']
        full = pane_cmd(s['tmux_session']).strip()
        r['launch'] = full[-300:]
        def replied():
            text = pane(s['tmux_session'])
            # First-run screens are the CLI's, not Lectern's: dismiss them the
            # way a person would, then the typed opening message follows.
            if '⎋ skip' in text:
                subprocess.run(['tmux', 'send-keys', '-t', '=' + s['tmux_session'] + ':', 'Escape'], env=env)
            return 'PROBE-REPLY' in text
        try:
            wait(replied, 45, name + ' reply')
        except RuntimeError:
            # Lectern types the opening message only once it recognises an
            # input prompt on screen; for a CLI whose prompt it does not
            # recognise, type it here the way a person would.
            r['typed_by_harness'] = True
            subprocess.run(['tmux', 'send-keys', '-t', '=' + s['tmux_session'] + ':', '-l', 'say hi'], env=env)
            time.sleep(1)
            subprocess.run(['tmux', 'send-keys', '-t', '=' + s['tmux_session'] + ':', 'Enter'], env=env)
            wait(replied, 45, name + ' reply')
        bound = wait(lambda: bound_id(s['id']), 90, name + ' binding')
        r['bound'] = bound
        convs = api(f"/sessions/{s['id']}/conversations")
        r['listed'] = [c['id'] for c in convs.get('conversations', [])]
        api(f"/sessions/{s['id']}", method='DELETE')
        wait(lambda: api(f"/sessions/{s['id']}").get('ended_at'), 30, name + ' stop')
        plan = next((x for x in api('/sessions/restorable?all=true') if x['id'] == s['id']), None)
        r['plan'] = plan and plan['action']
        out = api(f"/sessions/{s['id']}/reopen", {})
        n = out['session']
        r['restore'] = out['action']
        full = pane_cmd(n['tmux_session']).strip()
        r['restore_exact'] = bound in full
        r['restore_cmd'] = full[full.find('&& ' + name) if ('&& ' + name) in full else -300:][:300]
        # Prove the resumed CLI really has the earlier exchange: its next
        # request to the model must carry the first reply.
        time.sleep(8)
        before = open(PROBE + '/fake/requests.log').read().count('\n')
        subprocess.run(['tmux', 'send-keys', '-t', '=' + n['tmux_session'] + ':', '-l', 'again'], env=env)
        time.sleep(1)
        subprocess.run(['tmux', 'send-keys', '-t', '=' + n['tmux_session'] + ':', 'Enter'], env=env)
        def history_sent():
            lines = open(PROBE + '/fake/requests.log').read().splitlines()[before:]
            return any('saw_probe=True' in l for l in lines if l.startswith('POST'))
        try:
            r['resumed_with_history'] = bool(wait(history_sent, 40, name + ' resumed request'))
        except RuntimeError:
            r['resumed_with_history'] = False
        r['restore_pane_tail'] = [l for l in pane(n['tmux_session']).splitlines() if l.strip()][-4:]
        if catalog[name].get('fork_args'):
            f = api(f"/sessions/{n['id']}/fork", {'conversation_id': bound})
            full = pane_cmd(f['tmux_session']).strip()
            r['fork_exact'] = bound in full
            r['fork_cmd'] = full[-300:]
            try:
                fb = wait(lambda: bound_id(f['id']), 45, name + ' fork binding')
            except RuntimeError:
                fb = ''
            api(f"/sessions/{f['id']}", method='DELETE')
            if not fb:
                # some CLIs list a conversation only after its process exits
                try:
                    fb = wait(lambda: bound_id(f['id']), 45, name + ' fork binding after exit')
                    r['fork_bound_after_exit'] = True
                except RuntimeError:
                    fb = ''
            r['fork_bound'] = fb
        api(f"/sessions/{n['id']}", method='DELETE')
        wait(lambda: api(f"/sessions/{n['id']}").get('ended_at'), 30, name + ' stop 2')
        # the same restore through the command line
        cli = subprocess.run([LECTERN, 'restore', str(n['id']), '--no-attach'], env=dict(env, LECTERN_API=URL),
                             capture_output=True, text=True, timeout=120)
        r['cli_restore'] = (cli.stdout + cli.stderr).strip()[-200:]
        after = [x for x in api('/sessions') if x['id'] > n['id'] and x.get('agent') == name and not x.get('ended_at')]
        if after:
            full = pane_cmd(after[-1]['tmux_session']).strip()
            r['cli_restore_exact'] = bound in full
            api(f"/sessions/{after[-1]['id']}", method='DELETE')
        r['ok'] = r['restore_exact']
    except Exception as e:
        r['error'] = str(e)
        r['tmux_ls'] = subprocess.run(['tmux', 'ls'], env=env, capture_output=True, text=True).stdout
        try:
            r['pane'] = [l for l in pane(s['tmux_session']).splitlines() if l.strip()][-8:]
        except Exception:
            pass

print(json.dumps(results, indent=1))
proc.terminate(); proc.wait()
print('logs in', tmp)
