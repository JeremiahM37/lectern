#!/usr/bin/python3
"""Privileged launcher. Install root-owned; never expose _execute via sudoers."""
import argparse
import base64
import ctypes
from contextlib import contextmanager
import datetime
import fcntl
import hashlib
import gzip
import tempfile
import ipaddress
import json
import os
from pathlib import Path, PurePosixPath
import platform
import pwd
import re
import resource
import selectors
import shutil
import shlex
import stat
import subprocess
import sys
import time
import tarfile
import uuid

ROOT = Path('/mnt/bulk/lectern-autonomy/jobs')
INSTALL = '/usr/local/libexec/lectern-autonomy-runner'
EXPERT_PROBE_HELPER = Path('/usr/local/libexec/lectern-autonomy-expert-probe.py')
PRIVATE_INTEGRATION_HELPER = Path('/usr/local/libexec/lectern-autonomy-private-integration.py')
NODE_RUNTIME_HELPER = Path('/usr/local/libexec/autonomy-node-runtime.py')
SERVER_OPERATIONS_HELPER = Path('/usr/local/libexec/lectern-autonomy-server-operations.py')
SERVER_REGISTRY = Path('/etc/lectern/server-targets.json')
SERVER_OBSERVATIONS_ROOT = ROOT.parent / 'server-observations'
SERVER_MAINTENANCE_HELPER = Path('/usr/local/libexec/lectern-autonomy-server-maintenance.py')
SERVER_MAINTENANCE_INSPECT_HELPER = Path('/usr/local/libexec/lectern-autonomy-server-maintenance-inspect.py')
SERVER_MAINTENANCE_ROOT = ROOT.parent / 'server-maintenance'
SERVER_MAINTENANCE_TOOLS = ROOT.parent / 'server-maintenance-tools'
INTEGRATION_ROOT = ROOT.parent / 'integrations'
ASSET_CACHE = ROOT.parent / 'binary-cache'
DEPENDENCIES = ROOT.parent / 'dependencies'
AUTH_LOCK = Path('/run/lectern-autonomy-auth.lock')
ARTIFACT_LOCK = Path('/run/lectern-artifact.lock')
STORAGE_LIMIT = 200 * 1024**3
UID = GID = 65534
DENY = ['0.0.0.0/8', '10.0.0.0/8', '100.64.0.0/10',
        '169.254.0.0/16', '172.16.0.0/12', '192.168.0.0/16', '224.0.0.0/4',
        '240.0.0.0/4', '::/128', '::ffff:0:0/96', 'fc00::/7',
        'fe80::/10', 'ff00::/8']
AUTH = {'codex': Path('/home/admin/.codex/auth.json'),
        'claude': Path('/home/admin/.claude/.credentials.json')}
BIN = {'codex': Path('/home/admin/.local/bin/codex'),
       'claude': Path('/home/admin/.local/bin/claude')}

def codex_auth_fresh(auth, now=None):
    """Scheduling hint only; Codex/provider still authenticate the actual token."""
    now = time.time() if now is None else now
    try:
        refreshed = datetime.datetime.fromisoformat(auth['last_refresh'].replace('Z', '+00:00')).timestamp()
        token = auth['tokens']['access_token']
        claims = json.loads(base64.urlsafe_b64decode(token.split('.')[1] + '==='))
        return (auth.get('auth_mode') == 'chatgpt' and
                0 <= now - refreshed < 6 * 86400 and claims['exp'] > now + 3600)
    except (ValueError, TypeError, KeyError, IndexError):
        return False

def refresh_codex_auth():
    """Ask the trusted CLI, as admin, to refresh its own managed login.

    No worker output or worker credential file ever flows back to the host.
    Account responses can contain personal data: never include them in errors.
    This performs account maintenance only, without starting a model turn.
    """
    command = ['/usr/sbin/runuser', '-u', 'admin', '--', '/usr/bin/env',
               'HOME=/home/admin', 'CODEX_HOME=/home/admin/.codex',
               str(BIN['codex'].resolve(strict=True)), 'app-server', '--stdio']
    proc = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, env={'PATH': '/usr/bin:/bin'})
    deadline = time.monotonic() + 20
    pending = bytearray()
    selector = selectors.DefaultSelector()
    selector.register(proc.stdout, selectors.EVENT_READ)
    def send(message):
        proc.stdin.write((json.dumps(message) + '\n').encode()); proc.stdin.flush()
    def receive(expected):
        while time.monotonic() < deadline:
            while b'\n' in pending:
                line, _, rest = pending.partition(b'\n'); pending[:] = rest
                reply = json.loads(line)
                if reply.get('id') == expected:
                    if 'error' in reply or 'result' not in reply:
                        raise RuntimeError('Codex login refresh failed; host login requires attention')
                    return reply['result']
            if not selector.select(max(0, deadline - time.monotonic())):
                break
            chunk = os.read(proc.stdout.fileno(), 8192)
            if not chunk:
                break
            pending.extend(chunk)
            if len(pending) > 1024 * 1024:
                raise RuntimeError('Codex login refresh response exceeded limit')
        raise RuntimeError('Codex login refresh timed out or exited')
    try:
        send({'id': 1, 'method': 'initialize', 'params': {
            'clientInfo': {'name': 'lectern-auth-preflight', 'version': '1.0'}}})
        receive(1)
        send({'method': 'initialized'})
        send({'id': 2, 'method': 'account/read', 'params': {'refreshToken': True}})
        account = receive(2).get('account')
        if not isinstance(account, dict) or account.get('type') != 'chatgpt':
            raise RuntimeError('Codex workshop requires a managed ChatGPT login')
    finally:
        selector.close()
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill(); proc.wait()
        proc.stdin.close(); proc.stdout.close()

def codex_auth_snapshot():
    # Serialize workshop refreshes. Codex owns persistence and recovery against
    # concurrent native clients; never implement a second refresh-token writer.
    fd = os.open(AUTH_LOCK, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1:
            raise ValueError('untrusted workshop authentication lock')
        deadline = time.monotonic() + 8
        while True:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB); break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise RuntimeError('Codex login refresh lock timed out')
                time.sleep(.1)
        auth = json.loads(AUTH['codex'].read_text())
        if not codex_auth_fresh(auth):
            refresh_codex_auth()
            auth = json.loads(AUTH['codex'].read_text())
        if not codex_auth_fresh(auth):
            raise RuntimeError('Codex login refresh did not persist fresh managed credentials')
        # Workers need only the current access token, never the capability to
        # rotate the host's refresh token. Their lifetime is at most 30 minutes.
        auth['tokens']['refresh_token'] = ''
        return json.dumps(auth)
    finally:
        os.close(fd)

SELFTEST = r"""
import os, pathlib, socket, json
assert os.getuid() == 65534 and os.getgid() == 65534
assert os.getgroups() == []
assert not pathlib.Path('/home/admin').exists()
assert not pathlib.Path('/etc/shadow').exists()
assert not pathlib.Path('/run/systemd/private').exists()
assert pathlib.Path('/proc/1/comm').read_text().strip() != 'systemd'
status = pathlib.Path('/proc/self/status').read_text()
assert 'CapEff:\t0000000000000000' in status
assert 'NoNewPrivs:\t1' in status
try:
    pathlib.Path('/usr/autonomy-write-probe').write_text('bad')
except OSError:
    pass
else:
    raise AssertionError('host tools directory writable')
for fd in pathlib.Path('/proc/self/fd').iterdir():
    try:
        target = os.readlink(fd)
    except OSError:
        continue
    if int(fd.name) > 2:
        assert '/mnt/bulk/' not in target and '/home/admin/' not in target, target
s = socket.socket()
s.settimeout(2)
try:
    s.connect(('1.1.1.1',443))
except OSError:
    pass
else:
    raise AssertionError('direct public network unexpectedly reachable')
finally:
    s.close()
pathlib.Path('/work/selftest-artifact.txt').write_text('private worker wrote this')
pathlib.Path('/work/selftest-link').symlink_to('selftest-artifact.txt')
pathlib.Path('/work/selftest-host-link').symlink_to('/etc/shadow')
assert not pathlib.Path('/work/selftest-host-link').exists()
pathlib.Path('/work/autonomy-report.json').write_text('{"selftest":"PASS"}')
print(json.dumps({'selftest':'PASS','uid':os.getuid(),'host_files_hidden':True,
                  'no_capabilities':True,'direct_network_blocked':True}), flush=True)
"""

NETWORK_SELFTEST = r"""
import subprocess,time
for attempt in range(30):
    try:
        c=socket.create_connection(('127.0.0.1',18080),timeout=.2);c.close();break
    except OSError:
        time.sleep(.1)
else:
    raise AssertionError('local proxy listener did not become ready')
for url,expected in [('http://example.com','403'),
                     ('http://api.github.com/','403'),
                     ('http://127.0.0.1:9110/','403'),
                     ('http://100.100.100.100/','403'),
                     ('http://[::1]/','403'),('http://localhost/','403')]:
    result=subprocess.run(['/usr/bin/curl','-sS','-o','/dev/null','-w','%{http_code}',
                           '--max-time','15','--noproxy','','--proxy',
                           'http://127.0.0.1:18080',url],capture_output=True,text=True)
    assert result.returncode==0 and result.stdout==expected, (url,result.stdout,result.stderr)
print(json.dumps({'network_selftest':'PASS','arbitrary_public_http_blocked':True,
                  'private_ipv4_ipv6_cgnat_dns_blocked':True}),flush=True)
"""

def run(args, **kw):
    return subprocess.run(args, check=True, text=True, capture_output=True, **kw)

def job_path(value):
    if str(uuid.UUID(value)) != value:
        raise ValueError('job must be a canonical UUID')
    p = ROOT / value
    for part in [ROOT, *ROOT.parents, p]:
        if part.is_symlink():
            raise ValueError('symlink in job path')
    return p

def unit(job):
    job_path(job)
    return 'lectern-autonomy-' + job + '.service'

def regular(p):
    s = p.lstat()
    if not stat.S_ISREG(s.st_mode) or s.st_nlink != 1:
        raise ValueError('expected regular non-hardlinked file: ' + str(p))
    return s

def write_new(p, data, mode=0o640):
    fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    os.fchmod(fd, mode)
    with os.fdopen(fd, 'w') as f:
        f.write(data)
    os.chown(p, 0, pwd.getpwnam('admin').pw_gid)

def bpf_attached(cgroup):
    """Query effective cgroup packet filters, not merely configured properties."""
    nr = {'x86_64': 321, 'aarch64': 280}.get(platform.machine())
    if nr is None:
        raise RuntimeError('unsupported BPF query architecture')
    class Attr(ctypes.Structure):
        _fields_ = [('fd', ctypes.c_uint32), ('attach_type', ctypes.c_uint32),
                    ('query_flags', ctypes.c_uint32), ('attach_flags', ctypes.c_uint32),
                    ('prog_ids', ctypes.c_uint64), ('prog_cnt', ctypes.c_uint32)]
    fd = os.open(cgroup, os.O_RDONLY | os.O_DIRECTORY)
    try:
        for attach in (0, 1):  # BPF_CGROUP_INET_INGRESS / EGRESS
            ids = (ctypes.c_uint32 * 256)()
            a = Attr(fd, attach, 1, 0, ctypes.addressof(ids), 256)
            libc = ctypes.CDLL(None, use_errno=True)
            if libc.syscall(nr, 16, ctypes.byref(a), ctypes.sizeof(a)) != 0:
                raise OSError(ctypes.get_errno(), 'BPF_PROG_QUERY failed')
            if a.prog_cnt < 1:
                raise RuntimeError('missing effective cgroup IP filter')
    finally:
        os.close(fd)

def addresses():
    result = list(DENY)
    for dev in json.loads(run(['/usr/sbin/ip', '-json', 'address']).stdout):
        for addr in dev.get('addr_info', []):
            ip = ipaddress.ip_address(addr['local'])
            if not ip.is_loopback:
                result.append(str(ip) + ('/32' if ip.version == 4 else '/128'))
    return result

def properties():
    return ['Type=exec', 'KillMode=control-group', 'TimeoutStopSec=10s',
            'RuntimeMaxSec=1800', 'MemoryMax=4G', 'MemorySwapMax=0', 'CPUQuota=200%',
            'TasksMax=512', 'NoNewPrivileges=yes', 'RestrictSUIDSGID=yes',
            'ProtectControlGroups=yes', 'ProtectKernelModules=yes',
            'RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK',
            'IPAddressDeny=' + ' '.join(addresses()), 'UMask=0077',
            'LimitFSIZE=268435456']

def review_evidence_mount(work):
    evidence = Path(work) / '.lectern-review'
    if evidence.is_symlink() or (evidence.exists() and not evidence.is_dir()):
        raise ValueError('review evidence root must be a real directory')
    if evidence.exists():
        return ['--ro-bind', str(evidence), '/work/.lectern-review']
    return []

def bwrap(p, provider, assets, work, bridges, python_project=None, browser=None, python_test_key=None, node_project=None, node_runtime=None, go_runtime=None):
    assets = Path(assets)
    cmd = ['/usr/bin/bwrap', '--die-with-parent', '--new-session', '--unshare-user',
           '--unshare-pid', '--unshare-ipc', '--unshare-uts', '--unshare-net', '--cap-drop', 'ALL',
           '--clearenv', '--setenv', 'HOME', '/home/agent', '--setenv', 'PATH', '/usr/bin:/bin',
           '--setenv', 'LANG', 'C.UTF-8', '--setenv', 'TERM', 'xterm-256color',
           '--setenv', 'TMPDIR', '/tmp', '--tmpfs', '/', '--ro-bind', '/usr', '/usr']
    for path in ('/lib', '/lib64', '/bin', '/sbin'):
        if Path(path).exists():
            cmd += ['--ro-bind', path, path]
    for local in ('/usr/local/bin','/usr/local/sbin','/usr/local/etc'):
        if Path(local).exists():cmd += ['--tmpfs',local]
    bundle = go_dependency_bundle(Path(work)) if go_runtime is None else None
    if go_runtime is not None:
        cmd += go_runtime_mount(*go_runtime)
    if bundle is not None:
        cmd += ['--ro-bind', str(bundle / 'mod'), '/opt/go-modules',
                '--ro-bind', str(bundle / 'manifest.json'), '/opt/go-dependencies.json',
                '--setenv', 'GOMODCACHE', '/opt/go-modules',
                '--setenv', 'GOPATH', '/tmp/go', '--setenv', 'GOCACHE', '/tmp/go-build',
                '--setenv', 'GOPROXY', 'off', '--setenv', 'GOSUMDB', 'off',
                '--setenv', 'GOTOOLCHAIN', 'local', '--setenv', 'GOENV', 'off',
                '--setenv', 'GOWORK', 'off',
                '--setenv', 'PATH', '/usr/local/go/bin:/usr/bin:/bin']
    if python_project is None:
        cmd += python_test_runtime_mount(python_test_key)
    else:
        cmd += python_project_mount(python_project)
    cmd += ['--proc', '/proc', '--dev', '/dev', '--tmpfs', '/tmp', '--tmpfs', '/run',
            '--tmpfs', '/home', '--dir', '/home/agent', '--dir', '/etc',
            '--ro-bind', '/etc/ssl/certs', '/etc/ssl/certs',
            '--ro-bind', str(assets / 'resolv.conf'), '/etc/resolv.conf',
            '--ro-bind', str(assets / 'passwd'), '/etc/passwd',
            '--ro-bind', str(assets / 'group'), '/etc/group',
            '--ro-bind', str(assets / 'nsswitch.conf'), '/etc/nsswitch.conf',
                        '--ro-bind', str(assets / 'prompt.txt'), '/prompt.txt',
            '--bind', str(work), '/work', '--chdir', '/work']
    if browser is not None:
        cmd += browser_runtime_mount(browser)
    if node_project is not None:
        q=json.loads((p/'node-requirement.json').read_text())
        project_dir=str(PurePosixPath(q.get('package_json','package.json')).parent)
        destination='/work'+('/'+project_dir if project_dir!='.' else '')+'/node_modules'
        cmd += ['--ro-bind',str(node_runtime),'/opt/node',
                '--ro-bind',str(node_project/'payload/project'),'/opt/node-project',
                '--ro-bind',str(node_project/'payload/cache'),'/opt/node-cache',
                '--ro-bind',str(node_project/'payload/project/node_modules'),destination,
                '--setenv','PATH','/opt/node-project/node_modules/.bin:/opt/node/bin:'+('/opt/go-toolchain/bin:' if go_runtime else '/usr/local/go/bin:')+'/usr/bin:/bin',
                '--setenv','NODE_PATH','/opt/node-project/node_modules',
                '--setenv','NPM_CONFIG_CACHE','/tmp/node-cache',
                '--setenv','NPM_CONFIG_OFFLINE','true',
                '--setenv','NPM_CONFIG_USERCONFIG','/tmp/node-user.npmrc',
                '--setenv','NPM_CONFIG_GLOBALCONFIG','/tmp/node-global.npmrc',
                '--setenv','NPM_CONFIG_AUDIT','false','--setenv','NPM_CONFIG_FUND','false',
                '--setenv','NPM_CONFIG_UPDATE_NOTIFIER','false']
    cmd += review_evidence_mount(work)
    cmd += ['--ro-bind', str(bridges), '/bridges',
            '--symlink', '/bridges/network.sock', '/network.sock',
            '--symlink', '/bridges/bridge.sock', '/bridge.sock']
    for name in ('HTTP_PROXY', 'HTTPS_PROXY', 'http_proxy', 'https_proxy'):
        cmd += ['--setenv', name, 'http://127.0.0.1:18080']
    cmd += ['--setenv', 'NO_PROXY', '', '--setenv', 'no_proxy', '']
    model = json.loads((p / 'job.json').read_text()).get('model', '')
    model_args = ['--model', model] if model else []
    if provider != 'selftest':
        cmd += ['--ro-bind', str(assets / 'agent'), '/agent']
    if provider == 'selftest':
        hold = int(json.loads((p / 'job.json').read_text()).get('selftest_hold', 0))
        network_test = NETWORK_SELFTEST if json.loads((p / 'job.json').read_text()).get('network_selftest') else ''
        code = SELFTEST + network_test + '\nimport time; time.sleep(' + str(hold) + ')\n'
        if hold and network_test:
            code += network_test  # exercise a replacement controller socket
        cli = ['/usr/bin/python3', '-c', code]
    elif provider == 'codex':
        cmd += ['--ro-bind', str(assets / 'codex-code-mode-host'), '/codex-code-mode-host']
        cmd += ['--dir', '/home/agent/.codex', '--ro-bind', str(assets / 'auth'),
                '/home/agent/.codex/auth.json']
        cli = ['/agent', 'exec', '--json', '--skip-git-repo-check',
               '--dangerously-bypass-approvals-and-sandbox', *model_args, '-']
    else:
        cmd += ['--dir', '/home/agent/.claude', '--ro-bind', str(assets / 'auth'),
                '/home/agent/.claude/.credentials.json', '--setenv', 'DISABLE_AUTOUPDATER', '1',
                '--setenv', 'CLAUDE_CODE_PROXY_RESOLVES_HOSTS', '1']
        cli = ['/agent', '-p', '--verbose', '--output-format', 'stream-json',
               '--dangerously-skip-permissions', '--strict-mcp-config',
               '--mcp-config', '{"mcpServers":{}}', *model_args]
    node_setup='set -e\ncp -a /opt/node-cache /tmp/node-cache\nchmod -R u+w /tmp/node-cache\n' if node_project is not None else ''
    launch = node_setup + '/usr/bin/socat TCP4-LISTEN:18080,bind=127.0.0.1,reuseaddr,fork UNIX-CONNECT:/network.sock &\nexec ' + shlex.join(cli)
    # Explicitly close inherited host directory descriptors before any untrusted
    # process can walk '..' through one, even if bwrap changes its fd policy.
    return cmd + ['--', '/usr/bin/python3', '-c',
                  'import os,sys; os.closerange(3,65536); os.execv("/bin/sh", ["sh","-c",sys.argv[1]])', launch]

def execute(job):
    p = job_path(job)
    meta = p / 'job.json'
    if regular(meta).st_uid != 0:
        raise RuntimeError('untrusted job metadata')
    current = Path('/proc/self/cgroup').read_text().strip().split('::')[-1]
    if not current.endswith('/' + unit(job)):
        raise RuntimeError('execution outside matching job cgroup refused')
    bpf_attached('/sys/fs/cgroup' + current)
    job_config = json.loads(meta.read_text())
    provider = job_config['provider']
    expected_test_key = job_config.get('python_test_key') or None
    # Bubblewrap canonicalizes source paths, so a /proc/self/fd directory
    # reference would still traverse the protected host job parent. Stage
    # mounts in a *private* supervisor mount namespace instead; these mounts
    # are never visible to other host processes or sibling jobs.
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.unshare(0x00020000) != 0:  # CLONE_NEWNS
        raise OSError(ctypes.get_errno(), 'private staging mount namespace failed')
    run(['/usr/bin/mount', '--make-rprivate', '/'])
    run(['/usr/bin/mount', '-t', 'tmpfs', '-o', 'mode=0755,size=16m,nosuid,nodev',
         'autonomy-staging', '/tmp'])
    bridge_dir = p / 'bridges'
    if bridge_dir.is_symlink() or not bridge_dir.is_dir():
        raise RuntimeError('dedicated bridges directory required')
    if bridge_dir.stat().st_mode & 0o022:
        raise RuntimeError('bridges directory must not be group/world writable')
    for source in bridge_dir.iterdir():
        if source.name not in ('network.sock', 'bridge.sock') or not stat.S_ISSOCK(source.lstat().st_mode):
            raise RuntimeError('bridges directory may contain only scoped sockets')
    if not (bridge_dir / 'network.sock').exists():
        raise RuntimeError('scoped public egress socket required')
    for name in ('assets', 'work', 'bridges'):
        target = Path('/tmp') / name
        target.mkdir(mode=0o755)
        run(['/usr/bin/mount', '--bind', str(p / name), str(target)])
    go_runtime=go_worker_runtime(job,Path('/tmp/work'))
    if go_runtime is not None:
        mounted=[]
        for name,source in zip(('go-modules-runtime','go-toolchain-runtime'),go_runtime):
            target=Path('/tmp')/name;target.mkdir();run(['/usr/bin/mount','--bind',str(source),str(target)]);mounted.append(target)
        go_runtime=tuple(mounted)
    project_bundle = python_project_bundle(job)
    project_mount = None
    if project_bundle is not None:
        project_mount = Path('/tmp/python-project'); project_mount.mkdir()
        run(['/usr/bin/mount', '--bind', str(project_bundle), str(project_mount)])
    browser_mount=None
    if project_bundle is not None:
        project_manifest=json.loads((project_bundle/'manifest.json').read_text())
        if project_manifest.get('browser_key'):
            browser_source=browser_runtime(project_manifest['browser_key'],project_manifest['packages']['playwright'])
            browser_mount=Path('/tmp/browser-runtime');browser_mount.mkdir()
            run(['/usr/bin/mount','--bind',str(browser_source),str(browser_mount)])
    if expected_test_key:
        verify_expected_python_test(job, expected_test_key)
    if project_bundle is not None and expected_test_key:
        raise PythonUnsupported('exact tooling-only environment conflicts with project runtime')
    node_project=None
    if (p/'node-requirement.json').exists():
        try:node_project=node_call('bundle',job)
        except (OSError,ValueError) as error:
            node_call('validation_failure',job,error)
            raise
    node_mount=node_runtime_mount=None
    if node_project is not None:
        node_manifest=json.loads((node_project/'manifest.json').read_text())
        node_runtime,_=node_module(job).tooling(sys.modules[__name__],node_manifest['runtime_digest'])
        node_mount=Path('/tmp/node-project');node_mount.mkdir()
        node_runtime_mount=Path('/tmp/node-runtime');node_runtime_mount.mkdir()
        run(['/usr/bin/mount','--bind',str(node_project),str(node_mount)])
        run(['/usr/bin/mount','--bind',str(node_runtime/'runtime'),str(node_runtime_mount)])
    command = bwrap(p, provider, '/tmp/assets', '/tmp/work', '/tmp/bridges', project_mount, browser_mount, expected_test_key, node_mount, node_runtime_mount, go_runtime)
    def drop():
        os.setgroups([])
        os.setgid(GID)
        os.setuid(UID)
    with (p / 'assets/prompt.txt').open('rb') as prompt:
        usage_begin(job)
        process = subprocess.Popen(command, stdin=prompt, preexec_fn=drop, close_fds=True)
        while process.poll() is None:
            try:
                heartbeat = regular(p / 'heartbeat')
                fresh = 0 <= time.time() - heartbeat.st_mtime < 60
            except OSError:
                fresh = False
            expired = usage_elapsed(job, True)['elapsed_milliseconds'] >= json.loads((p/'job.json').read_text()).get('runtime_seconds', 1800)*1000
            if not fresh or expired:
                usage_finish(job)
                write_new(p / 'result.json', json.dumps({'state': 'failed', 'exit_code': 124,
                                                       'reason': 'worker runtime limit reached' if expired else 'controller heartbeat expired'}))
                # Exact job cgroup only, including this trusted supervisor. No
                # detached grandchildren may outlive the usage monitor.
                run(['/usr/bin/systemctl', 'kill', '--kill-whom=all', '--signal=SIGKILL', unit(job)])
                raise RuntimeError('job cgroup termination unexpectedly returned')
            time.sleep(.1)
        result = process
    usage_finish(job)
    write_new(p / 'result.json', json.dumps({'state': 'done' if result.returncode == 0 else 'failed',
                                           'exit_code': result.returncode}))
    return result.returncode

def ensure_work(p):
    """Recover a retained fixed-size image after reboot; never create/format it."""
    work = p / 'work'
    image = p / 'work.ext4'
    st = regular(image)
    if st.st_uid != 0 or st.st_size != 2 * 1024**3 or st.st_mode & 0o022:
        raise ValueError('workspace image must be root-owned bounded 2 GiB volume')
    if work.is_symlink() or not work.is_dir():
        raise ValueError('work must be a real prepared directory')
    if not work.is_mount():
        if any(work.iterdir()):
            raise ValueError('refusing to obscure nonempty unmounted work directory')
        run(['/usr/bin/mount', '-o', 'loop,nosuid,nodev', str(image), str(work)])
    mounted = json.loads(run(['/usr/bin/findmnt', '--json', '--mountpoint', str(work), '-o', 'SOURCE,FSTYPE,OPTIONS']).stdout)['filesystems'][0]
    if mounted['fstype'] != 'ext4' or not mounted['source'].startswith('/dev/loop'):
        raise ValueError('workspace must be bounded ext4 loop mount')
    backing = run(['/usr/sbin/losetup', '-n', '-O', 'BACK-FILE', mounted['source']]).stdout.strip()
    if Path(backing) != image:
        raise ValueError('unexpected workspace backing volume')
    return work


@contextmanager
def launch_lock(job, blocking=True):
    p = job_path(job)
    fd = os.open(p / 'launch.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_nlink != 1 or info.st_mode & 0o022:
            raise ValueError('unsafe launch lock')
        fcntl.flock(fd, fcntl.LOCK_EX | (0 if blocking else fcntl.LOCK_NB))
        yield fd
    finally:
        os.close(fd)


def launch_state_unlocked(job):
    p = job_path(job)
    recorded = False
    for name in ('start-intent.json', 'job.json'):
        path = p / name
        if path.exists() or path.is_symlink():
            info = regular(path)
            if info.st_uid != os.geteuid() or info.st_mode & 0o022 or info.st_size > 4096:
                raise ValueError('unsafe runner start receipt')
            try:
                receipt = json.loads(path.read_text())
            except (ValueError, UnicodeError) as exc:
                raise ValueError('invalid runner start receipt') from exc
            if not isinstance(receipt, dict) or (name == 'start-intent.json' and receipt != {'version': 1}) or (name == 'job.json' and (receipt.get('provider') not in ('codex', 'claude', 'selftest') or not isinstance(receipt.get('model'), str))):
                raise ValueError('invalid runner start receipt')
            recorded = True
    # Legacy starts may have failed before job.json was written. Never retry
    # their partially populated assets in place or discard the original evidence.
    assets = p / 'assets'
    if assets.exists() or assets.is_symlink():
        info = assets.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid():
            raise ValueError('unsafe runner startup footprint')
        recorded = True
    worker = status_unlocked(job)
    if worker['state'] == 'running':
        return {'state': 'running'}
    if worker['state'] not in ('done', 'failed', 'stopped'):
        raise ValueError('invalid runner status')
    if recorded or (p / 'stopped').exists() or (p / 'result.json').exists():
        return {'state': 'consumed', 'worker_state': worker['state']}
    return {'state': 'unused'}


def launch_state(job):
    try:
        with launch_lock(job, blocking=False):
            return launch_state_unlocked(job)
    except BlockingIOError:
        return {'state': 'launching'}


def start(args):
    expected_test_key = getattr(args, 'python_test_key', None)
    if expected_test_key:
        runtime = python_test_runtime_status(expected_test_key)
        if runtime['state'] != 'verified': return runtime
    duration = getattr(args, 'runtime_seconds', 1800)
    if type(duration) is not int or not 1 <= duration <= 1800: raise ValueError('runtime_seconds must be between 1 and 1800')
    with launch_lock(args.job) as launch_fd:
        phase = launch_state_unlocked(args.job)['state']
        if phase == 'running':
            return status_unlocked(args.job)
        if phase != 'unused':
            raise ValueError('job UUID has already been used; preserve it and prepare a fresh UUID')
        p = job_path(args.job)
        write_new(p / 'start-intent.json', json.dumps({'version': 1}))
        with (p / 'start-intent.json').open('rb') as marker:
            os.fsync(marker.fileno())
        fd = os.open(p, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
        return start_locked(args, launch_fd)


def start_locked(args, launch_fd):
    p = job_path(args.job)
    if not p.is_dir() or p.stat().st_uid not in (0, pwd.getpwnam('admin').pw_uid):
        raise ValueError('controller must create job directory first')
    if Path(args.prompt) != p / 'prompt.txt':
        raise ValueError('prompt must be job/prompt.txt')
    regular(p / 'prompt.txt')
    args.model = args.model or ''
    if len(args.model) > 120 or args.model.startswith('-'):
        raise ValueError('invalid model')
    if (p / 'job.json').exists():
        previous = status_unlocked(args.job)
        if previous['state'] == 'running':
            return previous
        raise ValueError('job UUID has already been used; preserve it and prepare a fresh UUID')
    auth_snapshot = codex_auth_snapshot() if args.provider == 'codex' else None
    work = ensure_work(p)
    # Symlinks are data inside a private mount namespace. Never follow them
    # while inspecting or changing ownership; hardlinks/special files are refused.
    items = [work, *work.rglob('*')]
    for item in items:
        s = item.lstat()
        if not (stat.S_ISDIR(s.st_mode) or stat.S_ISLNK(s.st_mode)):
            regular(item)
    assets = p / 'assets'
    assets.mkdir(mode=0o700)
    os.chown(p, 0, pwd.getpwnam('admin').pw_gid)
    p.chmod(0o770)
    for item in items:
        os.chown(item, UID, GID, follow_symlinks=False)
    if args.provider != 'selftest':
        binary = BIN[args.provider].resolve(strict=True)
        with binary.open('rb') as f:
            if f.read(4) != b'\x7fELF':
                raise ValueError('provider must be the native ELF CLI, not a host wrapper')
        shared_binary(binary, assets / 'agent')
        if args.provider == 'codex':
            helper = binary.parent / 'codex-code-mode-host'
            regular(helper)
            shared_binary(helper, assets / 'codex-code-mode-host')
            (assets / 'codex-code-mode-host').chmod(0o555)
        (assets / 'agent').chmod(0o555)
        if auth_snapshot is not None:
            (assets / 'auth').write_text(auth_snapshot)
        else:
            shutil.copyfile(AUTH[args.provider], assets / 'auth')
        (assets / 'auth').chmod(0o444)
    shutil.copyfile(p / 'prompt.txt', assets / 'prompt.txt')
    (assets / 'prompt.txt').chmod(0o444)
    for name, data in {'resolv.conf': 'nameserver 1.1.1.1\nnameserver 9.9.9.9\n',
                       'passwd': 'agent:x:65534:65534:Agent:/home/agent:/bin/sh\n',
                       'group': 'agent:x:65534:\n', 'nsswitch.conf': 'hosts: files dns\n'}.items():
        write_new(assets / name, data, 0o444)
    assets.chmod(0o755)
    # The trusted supervisor stages these in private mounts before dropping
    # UID. The host job parent remains inaccessible to nobody; admin may
    # replace its broker sockets when the controller restarts.
    write_new(p / 'job.json', json.dumps({'provider': args.provider, 'model': args.model, 'python_test_key': getattr(args, 'python_test_key', None), 'runtime_seconds': getattr(args, 'runtime_seconds', 1800),
                                               'selftest_hold': args.hold_seconds if args.provider == 'selftest' else 0,
                                               'network_selftest': args.network_selftest if args.provider == 'selftest' else False}))
    write_new(p / 'heartbeat', '', 0o600)
    os.chown(p / 'heartbeat', pwd.getpwnam('admin').pw_uid, pwd.getpwnam('admin').pw_gid)
    for name in ('output.jsonl', 'stderr.log'):
        write_new(p / name, '')
    cmd = ['/usr/bin/systemd-run', '--quiet', '--unit=' + unit(args.job)]
    worker_properties = [prop for prop in properties() if not prop.startswith('RuntimeMaxSec=')]
    worker_properties.append('RuntimeMaxSec=' + str(getattr(args, 'runtime_seconds', 1800) + 120))
    for prop in worker_properties + ['StandardOutput=append:' + str(p / 'output.jsonl'),
                                'StandardError=append:' + str(p / 'stderr.log')]:
        cmd += ['--property=' + prop]
    run(cmd + [INSTALL, '_execute', '--job', args.job], env={'PATH': '/usr/sbin:/usr/bin:/sbin:/bin'}, pass_fds=(launch_fd,))
    return {'state': 'running', 'exit_code': None}

def status(job):
    try:
        with launch_lock(job, blocking=False):
            return status_unlocked(job)
    except BlockingIOError:
        # A privileged launcher may outlive the controller that called it.
        # Do not export/copy/restart its work while it can still launch a unit.
        return {'state': 'running', 'exit_code': None}


def stop(job):
    observation_error = None
    if (job_path(job) / 'server-observations').exists() or (SERVER_OBSERVATIONS_ROOT / job).exists():
        try:
            observed = server_observation('server-observe-stop', job)
            if observed['state'] != 'stopped': observation_error = RuntimeError('server observations are still stopping')
        except Exception as error:
            observation_error = error
    with launch_lock(job):
        p = job_path(job)
        if not (p / 'stopped').exists():
            write_new(p / 'stopped', '')
        # The lock prevents a delayed start after this stop completes.
        # Query the unit directly: cancellation intent is not proof of termination.
        result = run(['/usr/bin/systemctl', 'show', unit(job), '--property=ActiveState', '--value'])
        active = result.stdout.strip()
        if active in ('active', 'activating', 'deactivating', 'reloading'):
            run(['/usr/bin/systemctl', 'stop', unit(job)])
            usage_finish(job, confirmed_stop=True)
        elif active not in ('inactive', 'failed'):
            raise RuntimeError('runner unit status unavailable during stop')
        result = status_unlocked(job)
        if observation_error is not None: raise observation_error
        return result


def usage_begin(job):
    p=job_path(job)
    completion_write(p/'worker-usage.json', {'schema_version':1, 'boot_id':Path('/proc/sys/kernel/random/boot_id').read_text().strip(), 'started_monotonic_ns':time.monotonic_ns(), 'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat()})


def usage_finish(job, confirmed_stop=False):
    p=job_path(job); path=p/'worker-usage.json'
    if not path.exists(): return
    value=completion_json(path)
    if 'elapsed_milliseconds' in value:return
    value.update(usage_elapsed(job, True), ended_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
    if confirmed_stop:value['usage_accounting']='monotonic_until_confirmed_stop'
    completion_write(path,value)


def usage_elapsed(job, active):
    p=job_path(job);path=p/'worker-usage.json'
    if not path.exists():return {'elapsed_milliseconds':0,'usage_accounting':'not_started'}
    value=completion_json(path)
    if 'elapsed_milliseconds' in value:return {name:value[name] for name in ('elapsed_milliseconds','usage_accounting')}
    maximum=json.loads((p/'job.json').read_text()).get('runtime_seconds',1800)*1000
    if active and value.get('boot_id')==Path('/proc/sys/kernel/random/boot_id').read_text().strip():
        elapsed=max(0,(time.monotonic_ns()-value['started_monotonic_ns'])//1000000)
        return {'elapsed_milliseconds':elapsed,'usage_accounting':'monotonic_process_interval'}
    return {'elapsed_milliseconds':maximum,'usage_accounting':'reserved_limit_unknown_end'}


def status_unlocked(job):
    result=status_raw_unlocked(job)
    if result['state']=='running':
        # Silence is observable, not proof of deadlock. Long tools and remote
        # reasoning may legitimately emit nothing; the runtime cap owns expiry.
        output=job_path(job)/'output.jsonl'
        if output.exists():
            result.update(output_idle_milliseconds=max(0,int((time.time()-regular(output).st_mtime)*1000)),
                          activity_scope='last provider event only; silence does not establish a stall')
    if (job_path(job)/'worker-usage.json').exists():
        result.update(usage_elapsed(job,result['state']=='running'))
    failure=job_path(job)/'python-test-runtime.json'
    if failure.exists():
        result['python_test_runtime']=completion_json(failure)
        if result['python_test_runtime'].get('executed') is False and not (job_path(job)/'worker-usage.json').exists():
            result.update(elapsed_milliseconds=0,usage_accounting='runtime_validation_before_model')
    go_receipt=job_path(job)/'go-runtime.json'
    if go_receipt.exists():
        result['go_runtime']=completion_json(go_receipt)
        if result['go_runtime'].get('executed') is False and not (job_path(job)/'worker-usage.json').exists():
            result.update(elapsed_milliseconds=0,usage_accounting='runtime_validation_before_model')
    node_failure=job_path(job)/'node-runtime.json'
    if node_failure.exists():
        result['node_runtime']=completion_json(node_failure)
        if result['node_runtime'].get('executed') is False and not (job_path(job)/'worker-usage.json').exists():
            result.update(elapsed_milliseconds=0,usage_accounting='runtime_validation_before_model')
    return result


def status_raw_unlocked(job):
    p = job_path(job)
    r = subprocess.run(['/usr/bin/systemctl', 'show', unit(job), '--property=ActiveState,ExecMainStatus,Result'],
                       text=True, capture_output=True)
    props = dict(line.split('=', 1) for line in r.stdout.splitlines() if '=' in line)
    if r.returncode != 0 or props.get('ActiveState') not in ('active', 'activating', 'deactivating', 'reloading', 'inactive', 'failed'):
        raise RuntimeError('runner unit status unavailable')
    if props.get('ActiveState') in ('active', 'activating', 'deactivating', 'reloading'):
        return {'state': 'running', 'exit_code': None}
    # A stop marker records intent, and result.json may precede process exit.
    # Neither can override a live/transitional unit or an uncertain status query.
    if (p / 'result.json').exists():
        return json.loads((p / 'result.json').read_text())
    if (p / 'stopped').exists():
        return {'state': 'stopped', 'exit_code': None}
    return {'state': 'failed', 'exit_code': int(props.get('ExecMainStatus', '1')) or 1}

def digest_file(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def immutable_asset(path):
    st = path.lstat()
    if not stat.S_ISREG(st.st_mode) or st.st_uid != os.geteuid() or st.st_mode & 0o222:
        raise ValueError('unsafe shared binary: ' + str(path))
    return st


def shared_binary(source, target):
    """Keep every job path and byte, sharing only immutable executable content."""
    ASSET_CACHE.mkdir(mode=0o755, exist_ok=True)
    st = ASSET_CACHE.lstat()
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.geteuid() or st.st_mode & 0o022:
        raise ValueError('unsafe binary cache directory')
    fd = os.open(ASSET_CACHE / '.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        digest = digest_file(source)
        cached = ASSET_CACHE / digest
        if cached.exists() or cached.is_symlink():
            immutable_asset(cached)
            if digest_file(cached) != digest:
                raise ValueError('shared binary checksum mismatch')
        else:
            fd, temporary = tempfile.mkstemp(dir=ASSET_CACHE, prefix='.copy-')
            os.close(fd)
            temporary = Path(temporary)
            try:
                shutil.copyfile(source, temporary)
                if digest_file(temporary) != digest:
                    raise ValueError('source binary changed during copy')
                temporary.chmod(0o555)
                os.replace(temporary, cached)
            finally:
                temporary.unlink(missing_ok=True)
        if target.exists():
            immutable_asset(target)
            if digest_file(target) != digest:
                raise ValueError('destination binary changed during compaction')
            if target.stat().st_ino == cached.stat().st_ino:
                return
        temporary = target.with_name('.shared-' + str(uuid.uuid4()))
        try:
            os.link(cached, temporary, follow_symlinks=False)
            os.replace(temporary, target)
        finally:
            temporary.unlink(missing_ok=True)


def compact_assets():
    before = allocated_storage()
    compacted = 0
    shared_inodes = set()
    if ASSET_CACHE.exists():
        for cached in ASSET_CACHE.iterdir():
            if len(cached.name) == 64 and all(c in '0123456789abcdef' for c in cached.name):
                st = immutable_asset(cached)
                if digest_file(cached) != cached.name:
                    raise ValueError('shared binary checksum mismatch')
                shared_inodes.add((st.st_dev, st.st_ino))
    for candidate in sorted(ROOT.iterdir()):
        try:
            p = job_path(candidate.name)
        except ValueError:
            continue
        if not (p / 'result.json').is_file() and not (p / 'stopped').is_file():
            continue
        state = run(['/usr/bin/systemctl', 'show', unit(p.name), '--property=ActiveState', '--value']).stdout.strip()
        if state in ('active', 'activating', 'deactivating'):
            continue
        assets = p / 'assets'
        if not assets.exists():
            continue
        st = assets.lstat()
        if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.geteuid() or st.st_mode & 0o022:
            raise ValueError('unsafe job assets directory')
        for name in ('agent', 'codex-code-mode-host'):
            path = assets / name
            if path.exists() or path.is_symlink():
                st = immutable_asset(path)
                if (st.st_dev, st.st_ino) in shared_inodes:
                    continue
                shared_binary(path, path)
                compacted += 1
    return {'files_compacted': compacted, 'bytes_reclaimed': max(0, before - allocated_storage())}


def storage_status():
    used = allocated_storage()
    free = shutil.disk_usage(ROOT).free
    return {'ready': used < STORAGE_LIMIT and free >= 20 * 1024**3,
            'allocated_bytes': used, 'limit_bytes': STORAGE_LIMIT, 'free_bytes': free}


def python_test_runtime_mount(key=None):
    # Optional tooling must not prevent unrelated workers from launching. An
    # invalid bundle is never mounted; its unavailability is explicit in-worker.
    try:
        bundle = python_test_bundle(key)
    except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
        if key: raise PythonUnsupported('expected Python tooling runtime unavailable: ' + str(error)[:200]) from error
        reason = str(error) if isinstance(error, ValueError) else type(error).__name__
        return ['--setenv', 'LECTERN_PYTHON_TEST_RUNTIME_STATUS', 'unavailable',
                '--setenv', 'LECTERN_PYTHON_TEST_RUNTIME_REASON', reason[:200]]
    if bundle is None:
        return ['--setenv', 'LECTERN_PYTHON_TEST_RUNTIME_STATUS', 'not_provisioned']
    return python_site_mount(bundle) + ['--ro-bind', str(bundle / 'site-packages'), '/opt/python-test',
            '--ro-bind', str(bundle / 'manifest.json'), '/opt/python-test-runtime.json',
            '--setenv', 'PYTHONDONTWRITEBYTECODE', '1',
            '--setenv', 'LECTERN_PYTHON_TEST_RUNTIME_STATUS', 'verified']


def python_test_diagnostic(error):
    if isinstance(error, FileNotFoundError): return 'missing'
    if 'interpreter mismatch' in str(error): return 'incompatible'
    return 'integrity'


def verify_expected_python_test(job, key):
    try:
        return python_test_bundle(key)
    except (OSError,ValueError,KeyError,TypeError,AttributeError) as error:
        value={'capability':'python_test_runtime','key':key,'state':'unavailable','executed':False,
               'scope':'full content validation before model execution','reason':str(error)[:300],'diagnostic':python_test_diagnostic(error)}
        completion_write(job_path(job)/'python-test-runtime.json',value)
        raise PythonUnsupported('expected Python tooling content unavailable: '+str(error)[:200]) from error


def python_test_runtime_status(key, job=None):
    if not isinstance(key, str) or len(key)!=64 or any(c not in '0123456789abcdef' for c in key):
        raise ValueError('exact Python tooling key must be SHA256')
    if job:
        failure=job_path(job)/'python-test-runtime.json'
        if failure.exists():
            value=completion_json(failure)
            if value.get('key')==key and value.get('capability')=='python_test_runtime': return value
    value={'capability':'python_test_runtime','key':key,'scope':'metadata preflight; full content verified before model execution'}
    try:
        python_test_bundle(key, full=False)
        return dict(value,state='verified')
    except (OSError,ValueError,KeyError,TypeError,AttributeError) as error:
        return dict(value,state='unavailable',reason=str(error)[:300],diagnostic=python_test_diagnostic(error))


def python_test_bundle(key=None, full=True):
    root = DEPENDENCIES / 'python'
    active = root / 'active.json'
    if key is None and not active.exists() and not active.is_symlink():
        return None
    def trusted(path, directory=False):
        st = path.lstat()
        expected = stat.S_ISDIR(st.st_mode) if directory else stat.S_ISREG(st.st_mode)
        if not expected or st.st_uid != 0 or st.st_mode & (0o022 if directory else 0o222):
            raise ValueError('unsafe Python runtime path')
        return st
    for path in (DEPENDENCIES, root): trusted(path, True)
    if key is None:
        if trusted(active).st_size > 1024: raise ValueError('oversized Python runtime selection')
        key = json.loads(active.read_text()).get('key', '')
    if len(key) != 64 or any(c not in '0123456789abcdef' for c in key):
        raise ValueError('invalid Python runtime key')
    bundle = root / key
    site = bundle / 'site-packages'
    trusted(bundle, True); trusted(site, True)
    manifest = bundle / 'manifest.json'
    if trusted(manifest).st_size > 2 * 1024**2: raise ValueError('oversized Python runtime manifest')
    data = json.loads(manifest.read_text())
    identity = dict(data); identity.pop('key', None)
    actual = hashlib.sha256(json.dumps(identity, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    if data.get('key') != key or actual != key or data.get('kind') != 'python-test-runtime' or data.get('schema_version') != 1 or data.get('checksum_verified') is not True:
        raise ValueError('Python runtime provenance mismatch')
    if data.get('runtime_family') != 'python' + '.'.join(map(str, sys.version_info[:2])):
        raise ValueError('Python runtime interpreter mismatch')
    if set(data.get('packages', {})) != {'pytest', 'pluggy', 'iniconfig', 'packaging', 'pygments'}:
        raise ValueError('unexpected Python runtime packages')
    rows = data.get('files', [])
    if not rows or len(rows) > 10000: raise ValueError('Python runtime file limit')
    if not full: return bundle
    seen = set(); total = 0
    for row in rows:
        name = row['path']; parts = name.split('/')
        if not name or name.startswith('/') or any(p in ('', '.', '..') for p in parts) or '\\' in name or name in seen:
            raise ValueError('unsafe Python runtime member')
        seen.add(name); item = site / name
        for parent in item.parents:
            if parent == site: break
            trusted(parent, True)
        st = trusted(item)
        total += st.st_size
        if total > 64 * 1024**2 or st.st_size != row['size'] or digest_file(item) != row['sha256']:
            raise ValueError('Python runtime content mismatch')
    actual_files = set()
    for item in site.rglob('*'):
        if item.is_dir() and not item.is_symlink(): trusted(item, True)
        else:
            trusted(item); actual_files.add(item.relative_to(site).as_posix())
    if actual_files != seen: raise ValueError('unlisted Python runtime content')
    return bundle


# Project dependencies are installed by a fixed data-only helper, never pip on
# the host. The resolver and imports run in separate, credential-free sandboxes.
PYTHON_HELPER = Path('/usr/local/libexec/lectern-python-project-dependencies.py')
PYTHON_PIP_SOURCE = Path('/home/admin/.venvs/verify/lib/python3.13/site-packages')
BROWSER_HELPER = Path('/usr/local/libexec/lectern-browser-runtime.py')
PYTHON_SANDBOX_FILE_LIMIT = 256 * 1024**2
PYTHON_PROVISIONER_FILE_LIMIT = 2 * 1024**3  # Trusted sparse workspace image; children lower their own cap.


class PythonUnsupported(ValueError):
    pass


def python_site_mount(bundle):
    """Use the interpreter's normal site integration, independent of PYTHONPATH.

    This runtime is admitted specifically for Debian's /usr Python interpreter.
    The verified local site takes precedence over the already-visible distro
    site. Those existing system packages remain available; this does not claim
    the entire interpreter environment is a reproducibly provisioned bundle.
    Project cwd/PYTHONPATH keep Python's ordinary precedence; -I still finds the
    verified site, while the deliberately site-disabled -S remains an opt-out.
    """
    version='.'.join(map(str,sys.version_info[:2]))
    selected='/usr/local/lib/python'+version+'/dist-packages'
    return ['--ro-bind',str(bundle/'site-packages'),selected]


def python_project_mount(bundle):
    # Both names expose the SAME unified immutable environment. Existing audited
    # commands referring to the original pytest path survive dependency recovery.
    result=python_site_mount(bundle)
    for prefix in ('python-project','python-test'):
        result+=['--ro-bind',str(bundle/'site-packages'),'/opt/'+prefix,
                 '--ro-bind',str(bundle/'manifest.json'),'/opt/'+prefix+'-runtime.json']
    return result+['--setenv','PYTHONDONTWRITEBYTECODE','1',
                   '--setenv','LECTERN_PYTHON_TEST_RUNTIME_STATUS','verified',
                   '--setenv','LECTERN_PYTHON_PROJECT_RUNTIME_STATUS','verified']


def browser_runtime(key, version, full=True):
    if not isinstance(key,str) or len(key)!=64 or any(c not in '0123456789abcdef' for c in key):raise PythonUnsupported('matching immutable browser runtime is unavailable')
    root=DEPENDENCIES/'browser'
    try:
        for path in (DEPENDENCIES,root):
            info=path.lstat()
            if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_mode&0o022:raise ValueError('unsafe browser dependency root')
        bundle=root/key
        helper=python_helper(BROWSER_HELPER)
        try:manifest=helper.verify(bundle,full=full)
        except helper.BrowserCompatibilityError as exc:raise PythonUnsupported(str(exc)+'; audited environment rebind required') from exc
    except FileNotFoundError as exc:
        raise PythonUnsupported('selected browser runtime is unavailable; restore the registered payload or audit a new environment') from exc
    if manifest['playwright_version']!=version:raise PythonUnsupported('immutable browser does not match Playwright version')
    return bundle


def browser_select(version):
    selector=DEPENDENCIES/'browser/active.json'
    if not selector.exists():raise PythonUnsupported('verified browser payload unavailable for Playwright '+version)
    info=regular(selector)
    if info.st_uid!=0 or info.st_mode&0o222 or info.st_size>16384:raise ValueError('unsafe browser selector')
    value=json.loads(selector.read_text())
    if value.get('schema_version')!=1:raise ValueError('unsupported browser selector')
    key=value.get('playwright',{}).get(version,'')
    browser_runtime(key,version)
    return key


def browser_runtime_mount(bundle):
    return ['--ro-bind',str(bundle),'/opt/browser-runtime',
            '--setenv','PLAYWRIGHT_BROWSERS_PATH','/opt/browser-runtime/browsers',
            '--setenv','LECTERN_BROWSER_RUNTIME_STATUS','verified',
            '--dir','/home/agent/.cache','--symlink','/opt/browser-runtime/browsers','/home/agent/.cache/ms-playwright']


BROWSER_PROBE=r'''
import sys,json,http.server,threading,socket,hashlib,zlib,struct,site
from pathlib import Path
site.addsitedir('/fetch/site-packages')
from playwright.sync_api import sync_playwright
manifest=json.loads(Path('/browser/manifest.json').read_text())
class Fixture(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200);self.send_header('Content-Type','text/html');self.end_headers()
  self.wfile.write(b'<iframe src="/frame"></iframe>' if self.path=='/' else b'<button id="fixture">offline</button>')
 def log_message(self,*args):pass
server=http.server.HTTPServer(('127.0.0.1',0),Fixture)
threading.Thread(target=server.serve_forever,daemon=True).start()
with sync_playwright() as api:
 browser=api.chromium.launch()
 context=browser.new_context();context.add_init_script('window.lecternFixture=42')
 page=context.new_page();page.goto('http://127.0.0.1:'+str(server.server_port))
 assert page.frames[1].evaluate('window.lecternFixture')==42
 assert page.frames[1].locator('#fixture').inner_text()=='offline'
 page.add_style_tag(content='html {background:rgb(12,34,56)} body {margin:0} iframe {margin-left:10px}')
 image=page.screenshot(path='/tmp/browser-fixture.png')
 assert image[:8]==b'\x89PNG\r\n\x1a\n'
 offset=8;compressed=b'';color=None
 while offset<len(image):
  size=struct.unpack('>I',image[offset:offset+4])[0];kind=image[offset+4:offset+8];data=image[offset+8:offset+8+size];offset+=12+size
  if kind==b'IHDR':color=data[9];assert data[8]==8
  if kind==b'IDAT':compressed+=data
 assert color in (2,6) and zlib.decompress(compressed)[1:4]==bytes((12,34,56))
 assert browser.version==manifest['browser_version']
 version=browser.version;browser.close()
try:socket.create_connection(('1.1.1.1',443),timeout=.2)
except OSError:pass
else:raise AssertionError('unexpected browser probe egress')
assert not Path('/home/admin').exists() and not Path('/dependency.sock').exists()
Path('/proof/browser.json').write_text(json.dumps({'browser_key':manifest['key'],'browser_version':version,'network':'unshared-no-socket','fixture':True}))
'''


def browser_probe_receipt(path,key):
    try:
        info=regular(path)
        if info.st_size>32768:raise ValueError('oversized browser proof')
        proof=json.loads(path.read_text())
        if proof.get('browser_key')!=key or proof.get('network')!='unshared-no-socket' or proof.get('fixture') is not True:raise ValueError('incomplete browser proof')
        return proof
    except (ValueError,OSError,TypeError) as exc:raise PythonUnsupported('unsafe or incomplete offline browser proof') from exc


def node_module(job=None):
    path=NODE_RUNTIME_HELPER
    if job is not None:
        frozen=job_path(job)/'node-prerequisite/tools'
        if (frozen/'manifest.json').exists():
            records=completion_json(frozen/'manifest.json')
            if set(records)!={'autonomy-node-runtime.py','node-project-dependencies.py','node-dependencies.py','autonomy-runner.py'}:
                raise ValueError('incomplete Node executable manifest')
            for name,digest in records.items():
                target=frozen/name;info=regular(target)
                if info.st_uid!=0 or info.st_mode&0o222 or digest_file(target)!=digest:
                    raise ValueError('frozen Node executable changed before import')
            path=frozen/'autonomy-node-runtime.py'
    return python_helper(path)

def node_call(command,job,*args):
    return getattr(node_module(job),command )(sys.modules[__name__],job,*args)

def python_helper(path=None):
    import importlib.util
    path = PYTHON_HELPER if path is None else path
    info = regular(path)
    if info.st_uid != 0 or info.st_mode & 0o022:
        raise ValueError('untrusted Python dependency helper')
    spec = importlib.util.spec_from_file_location('lectern_python_dependencies', path)
    module = importlib.util.module_from_spec(spec)
    previous = sys.dont_write_bytecode; sys.dont_write_bytecode = True
    try: spec.loader.exec_module(module)
    finally: sys.dont_write_bytecode = previous
    return module


def python_private(path):
    path.mkdir(mode=0o700, exist_ok=True)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise ValueError('unsafe private Python dependency directory')
    return path


def python_request(job):
    p = job_path(job); source = p / 'python-requirement.json'
    info = regular(source)
    if info.st_size > 16384 or info.st_mode & 0o022 or info.st_uid not in (0,pwd.getpwnam('admin').pw_uid):
        raise ValueError('unsafe Python requirement envelope')
    request = json.loads(source.read_text())
    required={'schema_version','kind','requirements','imports','source_job','source_archive_sha256','admission_sha256'}
    optional={'expected_input_key','expected_bundle_key'}
    if not required.issubset(request) or set(request)-required-optional or request['schema_version'] != 1 or request['kind'] != 'python_wheels':
        raise ValueError('unsupported Python requirement envelope')
    job_path(request['source_job'])
    expected=[request.get(k,'') for k in ('expected_input_key','expected_bundle_key')]
    if bool(expected[0])!=bool(expected[1]):raise ValueError('incomplete inherited Python identity')
    for field in ('source_archive_sha256','admission_sha256',*([ 'expected_input_key','expected_bundle_key'] if expected[0] else [])):
        value = request[field]
        if not isinstance(value,str) or len(value)!=64 or any(c not in '0123456789abcdef' for c in value): raise ValueError('invalid Python source binding')
    for field in ('requirements','imports'):
        values = request[field]
        if not isinstance(values,list) or not 0<len(values)<=32 or any(not isinstance(x,str) or not x or len(x)>512 or '\n' in x or '\x00' in x for x in values):
            raise ValueError('invalid bounded Python requirements')
        request[field] = sorted(set(values))
    if archive_identity(request['source_job'])['sha256'] != request['source_archive_sha256']:
        raise ValueError('Python request source archive identity mismatch')
    stage = python_private(p / 'python-prerequisite')
    sealed = stage / 'request.json'
    if sealed.exists():
        if completion_json(sealed) != request: raise ValueError('Python requirement changed after admission')
    else: completion_write(sealed,request)
    return stage, request


def python_freeze_helper(stage, source):
    info=regular(source)
    if info.st_uid!=0 or info.st_mode&0o022 or info.st_size>256*1024:raise ValueError('untrusted Python helper snapshot source')
    target=stage/'helper.py'
    if not target.exists():
        temporary=stage/'helper.pending'
        if temporary.exists():regular(temporary);temporary.unlink()
        with temporary.open('xb') as out:
            out.write(source.read_bytes());out.flush();os.fsync(out.fileno())
        temporary.chmod(0o555);os.chown(temporary,0,0);os.replace(temporary,target)
    info=regular(target)
    if info.st_uid!=0 or info.st_mode&0o222:raise ValueError('untrusted frozen Python helper')
    return target


def python_cached_identity(key, bundle_key, request):
    if any(len(value)!=64 or any(c not in '0123456789abcdef' for c in value) for value in (key,bundle_key)):
        raise ValueError('invalid inherited Python cache key')
    cache=DEPENDENCIES/'python-project'
    for directory in (DEPENDENCIES,cache):
        info=directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_mode&0o022:raise ValueError('unsafe inherited Python cache')
    identity=completion_json(cache/(key+'.identity.json'))
    semantic={k:identity[k] for k in ('requirements','imports','tooling_requirements','runtime_digest')}
    if identity.get('input_key')!=key or hashlib.sha256(json.dumps(semantic,sort_keys=True,separators=(',',':')).encode()).hexdigest()!=key or identity['requirements']!=request['requirements'] or identity['imports']!=request['imports']:
        raise ValueError('inherited Python semantic identity mismatch')
    if hashlib.sha256(json.dumps(identity['runtime'],sort_keys=True,separators=(',',':')).encode()).hexdigest()!=identity['runtime_digest']:
        raise ValueError('inherited Python runtime identity mismatch')
    index=completion_json(cache/(key+'.json'))
    if index.get('bundle_key')!=bundle_key:raise ValueError('inherited Python bundle selection mismatch')
    return identity


def python_identity(stage, request):
    target=stage/'identity.json'
    if target.exists(): return completion_json(target)
    expected=request.get('expected_input_key','')
    if expected:
        result=python_cached_identity(expected,request['expected_bundle_key'],request)
        frozen=python_freeze_helper(stage,DEPENDENCIES/'python-project'/(expected+'.helper.py'))
        if digest_file(frozen)!=result['runtime']['helper_sha256']:raise ValueError('inherited Python helper identity mismatch')
        completion_write(target,result)
        return result
    frozen=python_freeze_helper(stage,PYTHON_HELPER)
    helper=python_helper(frozen)
    base,manifest=python_toolkit_identity()
    records=list(PYTHON_PIP_SOURCE.glob('pip-*.dist-info/RECORD'))
    if len(records)!=1: raise PythonUnsupported('trusted pip resolver unavailable; exactly one installed resolver is required')
    regular(records[0])
    runtime={'python_sha256':digest_file(Path('/usr/bin/python3').resolve()),
             'python_version':platform.python_version(), 'helper_sha256':digest_file(frozen),
             'pip_record_sha256':digest_file(records[0]), 'pytest_key':manifest['key'], 'policy':helper.POLICY}
    if helper.POLICY=='pypi-compatible-wheel-v2':runtime['target']=helper.target_identity(PYTHON_PIP_SOURCE)
    runtime_digest=hashlib.sha256(helper.canonical(runtime)).hexdigest()
    semantic={'requirements':request['requirements'],'imports':request['imports'],
              'tooling_requirements':[name+'=='+version for name,version in sorted(manifest['packages'].items())],
              'runtime_digest':runtime_digest}
    result=dict(semantic,input_key=hashlib.sha256(helper.canonical(semantic)).hexdigest(),runtime=runtime)
    completion_write(target,result)
    return result


def python_toolkit_identity():
    root=DEPENDENCIES/'python'
    for path in (DEPENDENCIES,root):
        info=path.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_mode&0o022: raise ValueError('unsafe Python toolkit directory')
    active=root/'active.json';info=regular(active)
    if info.st_uid!=0 or info.st_mode&0o222 or info.st_size>1024: raise ValueError('unsafe Python toolkit selector')
    key=json.loads(active.read_text()).get('key','')
    if len(key)!=64 or any(c not in '0123456789abcdef' for c in key):raise ValueError('invalid Python toolkit key')
    base=root/key;info=base.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_mode&0o022:raise ValueError('unsafe Python toolkit bundle')
    path=base/'manifest.json';info=regular(path)
    if info.st_uid!=0 or info.st_mode&0o222 or info.st_size>2*1024**2:raise ValueError('unsafe Python toolkit manifest')
    manifest=json.loads(path.read_text());body=dict(manifest);body.pop('key',None)
    if hashlib.sha256(json.dumps(body,sort_keys=True,separators=(',',':')).encode()).hexdigest()!=key or manifest.get('key')!=key or manifest.get('checksum_verified') is not True or set(manifest.get('packages',{}))!={'pytest','pluggy','iniconfig','packaging','pygments'}:raise ValueError('Python toolkit provenance mismatch')
    return base,manifest


def python_unit(job): return 'lectern-python-dependencies-'+job+'.service'


def python_active(job):
    return completion_service_active(python_unit(job))


def python_binding(request, identity):
    return dict(capability='python_wheels',input_key=identity['input_key'],runtime_digest=identity['runtime_digest'],
                **{k:request[k] for k in ('source_job','source_archive_sha256','admission_sha256')})


def python_dependencies(job):
    if status(job)['state']=='running': raise ValueError('Python preparation requires a stopped worker')
    stage,request=python_request(job)
    cancellation=(stage/'cancel.json').read_bytes() if (stage/'cancel.json').exists() else b''
    with (stage/'guard').open('a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX)
        try:
            identity=python_identity(stage,request)
        except (FileNotFoundError,PythonUnsupported) as exc:
            # Optional capability absence cannot fail the whole workshop. The
            # source envelope was independently validated before entering here.
            receipt=dict(capability='python_wheels',state='unavailable',unsupported=True,initialization_unavailable=True,
                         reason='Trusted Python prerequisite runtime unavailable: '+str(exc)[:250],
                         **{k:request[k] for k in ('source_job','source_archive_sha256','admission_sha256')})
            completion_write(stage/'receipt.json',receipt)
            return receipt
        binding=python_binding(request,identity)
        (stage/'heartbeat').touch()
        receipt=completion_json(stage/'receipt.json') if (stage/'receipt.json').exists() else dict(binding)
        if receipt.get('initialization_unavailable'):
            receipt=dict(binding)
        if any(receipt.get(k)!=v for k,v in binding.items()): raise ValueError('Python recovery binding mismatch')
        if receipt.get('state')=='verified':
            # Full bytes are reverified by the trusted launcher before mounting.
            try:python_project_bundle(job, full=False)
            except PythonUnsupported as exc:
                receipt.update(state='unavailable',unsupported=True,reason=str(exc)[:400])
                completion_write(stage/'receipt.json',receipt)
            return receipt
        if cancellation!=((stage/'cancel.json').read_bytes() if (stage/'cancel.json').exists() else b''):
            return dict(receipt,state='waiting',reason='Python launch cancelled by controller')
        if python_active(job): return dict(receipt,state='recovering')
        if receipt.get('state')=='recovering':
            receipt.update(state='waiting',reason='Python provisioner interrupted',retry_at=time.time()+30)
            completion_write(stage/'receipt.json',receipt)
        if receipt.get('unsupported'): return dict(receipt,state='unavailable')
        if receipt.get('attempts',0)>=3:
            if time.time()-receipt.get('started_at',0)<21600: return dict(receipt,state='unavailable')
            receipt.update(attempts=0,retry_at=0)
        if receipt.get('retry_at',0)>time.time(): return dict(receipt,state='waiting')
        if shutil.disk_usage(ROOT).free<24*1024**3:
            return dict(receipt,state='waiting',reason='Python provisioning needs 4 GiB above the storage floor')
        receipt.update(state='recovering',attempts=receipt.get('attempts',0)+1,started_at=time.time())
        completion_write(stage/'receipt.json',receipt)
        run(['/usr/bin/systemd-run','--quiet','--collect','--unit='+python_unit(job),
             '--property=RuntimeMaxSec=600','--property=MemoryMax=2G','--property=MemorySwapMax=0',
             '--property=LimitFSIZE='+str(PYTHON_PROVISIONER_FILE_LIMIT),'--property=CPUQuota=200%','--property=TasksMax=128','--property=KillMode=control-group',
             '--property=PrivateMounts=yes','--property=UMask=0077',INSTALL,'_python-dependencies','--job',job],pass_fds=(lock.fileno(),))
        return receipt


def python_dependencies_stop(job):
    stage=job_path(job)/'python-prerequisite'
    if stage.exists():
        python_private(stage)
        completion_write(stage/'cancel.json',{'epoch':str(uuid.uuid4())})
        with (stage/'guard').open('a') as lock:
            fcntl.flock(lock,fcntl.LOCK_EX)
            if python_active(job): run(['/usr/bin/systemctl','stop',python_unit(job)],pass_fds=(lock.fileno(),))
            if python_active(job): raise RuntimeError('Python provisioner did not stop')
            if (stage/'receipt.json').exists():
                receipt=completion_json(stage/'receipt.json')
                if receipt.get('state')=='recovering':
                    receipt.update(state='waiting',reason='Python provisioning interrupted by controller',retry_at=0,
                                   attempts=max(0,receipt.get('attempts',1)-1))
                    completion_write(stage/'receipt.json',receipt)
    return {'state':'stopped'}


def python_project_bundle(job, full=True):
    p=job_path(job)
    if not (p/'python-requirement.json').exists(): return None
    stage,request=python_request(job);identity=completion_json(stage/'identity.json');receipt=completion_json(stage/'receipt.json')
    binding=python_binding(request,identity)
    if receipt.get('state')!='verified' or any(receipt.get(k)!=v for k,v in binding.items()): raise ValueError('Python project prerequisites not verified')
    if request.get('expected_input_key') and (receipt.get('input_key')!=request['expected_input_key'] or receipt.get('bundle_key')!=request['expected_bundle_key']):raise ValueError('inherited Python environment changed')
    key=receipt.get('bundle_key','')
    if len(key)!=64 or any(c not in '0123456789abcdef' for c in key): raise ValueError('invalid Python project bundle key')
    root=DEPENDENCIES/'python-project';bundle=root/key
    for directory in (DEPENDENCIES,root,bundle,bundle/'site-packages'):
        info=directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_mode&0o022: raise ValueError('unsafe Python project bundle')
    manifest_path=bundle/'manifest.json';info=regular(manifest_path)
    if info.st_uid!=0 or info.st_mode&0o222 or info.st_size>32*1024**2: raise ValueError('unsafe Python project manifest')
    manifest=json.loads(manifest_path.read_text());body=dict(manifest);body.pop('key',None)
    if hashlib.sha256(json.dumps(body,sort_keys=True,separators=(',',':')).encode()).hexdigest()!=key or manifest.get('key')!=key or manifest.get('input_key')!=identity['input_key'] or manifest.get('runtime_digest')!=identity['runtime_digest'] or manifest.get('checksum_verified') is not True:
        raise ValueError('Python project manifest identity mismatch')
    def unavailable(reason):
        receipt.update(state='unavailable',unsupported=True,reason=reason[:400])
        completion_write(stage/'receipt.json',receipt)
        raise PythonUnsupported(reason)
    if manifest.get('packages',{}).get('playwright'):
        try:browser_runtime(manifest.get('browser_key',''),manifest['packages']['playwright'],full=full)
        except PythonUnsupported as exc:
            if full:unavailable(str(exc))
            raise
        if receipt.get('browser_key')!=manifest['browser_key']:raise ValueError('Python browser receipt differs from immutable bundle')
    elif manifest.get('browser_key') or receipt.get('browser_key'):raise ValueError('unexpected browser binding')
    if full:
        if identity['runtime'].get('policy')=='pypi-compatible-wheel-v2' and python_helper(stage/'helper.py').target_identity(PYTHON_PIP_SOURCE)!=identity['runtime'].get('target'):unavailable('native Python target changed; audited rebind required')
        if digest_file(Path('/usr/bin/python3').resolve())!=identity['runtime']['python_sha256']: unavailable('Python interpreter changed since dependency verification; audited rebind required')
        seen=set();total=0;site=bundle/'site-packages'
        rows=manifest.get('files',[])
        if not rows or len(rows)>100000: raise ValueError('Python project inventory size')
        for row in rows:
            name=row['path'];parts=name.split('/')
            if not name or name.startswith('/') or '\\' in name or any(c in ('','.','..') for c in parts) or name in seen: raise ValueError('invalid Python project member')
            seen.add(name);path=site/name
            for parent in path.parents:
                if parent==site:break
                pi=parent.lstat()
                if not stat.S_ISDIR(pi.st_mode) or pi.st_uid!=0 or pi.st_mode&0o222: raise ValueError('unsafe Python project directory')
            st=regular(path);total+=st.st_size
            if row.get('mode',0o444) not in (0o444,0o555) or stat.S_IMODE(st.st_mode)!=row.get('mode',0o444) or st.st_uid!=0 or st.st_size!=row['size'] or digest_file(path)!=row['sha256'] or total>1024**3: raise ValueError('Python project bytes changed')
        actual=set()
        for path in site.rglob('*'):
            info=path.lstat()
            if stat.S_ISDIR(info.st_mode):
                if info.st_uid!=0 or info.st_mode&0o222:raise ValueError('unsafe Python bundle directory')
            else: regular(path);actual.add(path.relative_to(site).as_posix())
        if seen!=actual:raise ValueError('Python project inventory mismatch')
    return bundle


def python_sandbox_command(command, probe=False):
    cmd=['/usr/bin/bwrap','--die-with-parent','--new-session','--unshare-all','--cap-drop','ALL',
         '--clearenv','--tmpfs','/','--ro-bind','/usr','/usr']
    for path in ('/lib','/lib64','/bin'):
        if Path(path).exists():cmd+=['--ro-bind',path,path]
    cmd+=['--proc','/proc','--dev','/dev','--tmpfs','/tmp','--dir','/etc',
          '--ro-bind','/etc/ssl/certs','/etc/ssl/certs',
          '--ro-bind','/tmp/python-helper','/helper.py','--ro-bind','/tmp/python-tooling','/tooling',
          '--ro-bind' if probe else '--bind','/tmp/python-fetch','/fetch',
          '--setenv','HOME','/tmp','--setenv','PATH','/usr/bin:/bin','--setenv','PYTHONDONTWRITEBYTECODE','1','--chdir','/tmp']
    if probe:cmd+=['--bind','/tmp/python-proof','/proof']
    else:cmd+=['--ro-bind','/tmp/python-dependency.sock','/dependency.sock']
    launch=('/usr/bin/socat TCP4-LISTEN:18080,bind=127.0.0.1,reuseaddr,fork UNIX-CONNECT:/dependency.sock &\nsleep .2\n' if not probe else '')
    if command=='browser-probe':
        cmd+=['--ro-bind','/tmp/python-browser','/browser','--setenv','PLAYWRIGHT_BROWSERS_PATH','/browser/browsers']
        launch+='exec /usr/bin/python3 -I -S -c '+shlex.quote(BROWSER_PROBE)
    else:launch+='exec /usr/bin/python3 -I -S /helper.py '+command
    return cmd+['--','/bin/sh','-c',launch]


def python_sandbox_run(job,stage,command,probe=False):
    def drop():
        resource.setrlimit(resource.RLIMIT_FSIZE,(PYTHON_SANDBOX_FILE_LIMIT,PYTHON_SANDBOX_FILE_LIMIT))
        os.setgroups([]);os.setgid(GID);os.setuid(UID)
    with (stage/(command+'.log')).open('w') as log:
        process=subprocess.Popen(python_sandbox_command(command,probe),preexec_fn=drop,close_fds=True,stdout=log,stderr=log)
        try:
            deadline=time.monotonic()+480
            while process.poll() is None:
                if time.monotonic()>deadline or not 0<=time.time()-(stage/'heartbeat').stat().st_mtime<60:
                    raise RuntimeError('Python controller heartbeat expired or bounded step timed out')
                time.sleep(1)
        finally:
            if process.poll() is None:
                # Kill the entire fixed unit, including detached resolver children.
                run(['/usr/bin/systemctl','kill','--kill-whom=all','--signal=SIGKILL',python_unit(job)])
        if process.returncode:
            exception=(PythonUnsupported('requested wheels or import environment are unsupported') if (process.returncode==2 or (command=='browser-probe' and process.returncode>0))
                       else RuntimeError('Python '+command+' failed'))
            exception.diagnostic=python_failure_diagnostic(stage/(command+'.log'))
            raise exception


def python_failure_diagnostic(path):
    info=regular(path)
    if info.st_uid!=os.geteuid() or info.st_mode&0o022 or info.st_size>PYTHON_SANDBOX_FILE_LIMIT:
        raise ValueError('unsafe Python diagnostic log')
    with path.open('rb') as log:
        log.seek(max(0,info.st_size-12000))
        raw=log.read(12000).decode('utf-8',errors='replace')
    # Fixed credential-free sandbox only. Retain evidence as labelled text, never
    # parse package output as a privileged command or a verification receipt.
    cleaned=''.join(c for c in raw if c in '\n\t' or c.isprintable())
    cleaned=cleaned.encode('utf-8')[:12000].decode('utf-8',errors='ignore')
    return 'Untrusted isolated prerequisite output (last 12000 bytes):\n'+cleaned


def python_probe_receipt(path, identity, request):
    # Imported package code owns this output. Validate the filesystem object
    # before root opens it: symlinks/FIFOs must never become host reads.
    try:
        info=regular(path)
        if info.st_size>32768:raise ValueError('offline import proof exceeds limit')
        observed=json.loads(path.read_text())
        wanted=sorted(set(request['imports']+['pytest']))
        rows=observed.get('imports',[])
        if observed.get('input_key')!=identity['input_key'] or observed.get('network')!='unshared-no-socket' or observed.get('python_version')!=identity['runtime']['python_version'] or not isinstance(rows,list) or len(rows)!=len(wanted) or sorted(x['module'] for x in rows)!=wanted:
            raise ValueError('incomplete offline import proof')
        if any(not isinstance(row.get('origin'),str) or not row['origin'].startswith('/bundle/site-packages/') for row in rows):
            raise ValueError('unexpected import origin')
        return observed
    except (ValueError,KeyError,TypeError,OSError) as exc:
        raise PythonUnsupported('unsafe or incomplete offline import proof') from exc


def python_dependencies_execute(job):
    current=Path('/proc/self/cgroup').read_text().strip().split('::')[-1]
    if not current.endswith('/'+python_unit(job)):raise ValueError('Python provisioning outside matching unit')
    stage,request=python_request(job);identity=completion_json(stage/'identity.json');receipt=completion_json(stage/'receipt.json')
    DEPENDENCIES.mkdir(mode=0o755,exist_ok=True)
    helper=python_helper(stage/'helper.py')
    with (DEPENDENCIES/'.provision.lock').open('a') as lock:
        try:fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:
            receipt.update(state='waiting',reason='another prerequisite is being provisioned',retry_at=time.time()+30,attempts=max(0,receipt.get('attempts',1)-1))
            completion_write(stage/'receipt.json',receipt);return 0
        try:
            if shutil.disk_usage(ROOT).free<24*1024**3 or allocated_storage()>STORAGE_LIMIT-4*1024**3:raise RuntimeError('insufficient Python prerequisite storage headroom')
            if digest_file(stage/'helper.py')!=identity['runtime']['helper_sha256']:raise ValueError('frozen Python helper changed after admission')
            if digest_file(Path('/usr/bin/python3').resolve())!=identity['runtime']['python_sha256']:raise PythonUnsupported('Python interpreter changed after admission; audited environment rebind required')
            cache=python_private(DEPENDENCIES/'python-project');index=cache/(identity['input_key']+'.json')
            if index.exists():
                cached=completion_json(index)
                receipt.update(state='verified',bundle_key=cached['bundle_key'],lock_sha256=cached['lock_sha256'],proof_sha256=cached['proof_sha256'])
                if cached.get('browser_key'):receipt['browser_key']=cached['browser_key']
                completion_write(stage/'receipt.json',receipt)
                python_project_bundle(job)
                return 0
            dependency_volume(stage);fetch=ensure_work(stage)
            tooling=stage/'tooling'
            if tooling.exists():shutil.rmtree(tooling)
            tool_identity=helper.snapshot_pip(PYTHON_PIP_SOURCE,tooling)
            if tool_identity['record_sha256']!=identity['runtime']['pip_record_sha256']:raise PythonUnsupported('pip resolver changed after admission; audited environment rebind required')
            if helper.POLICY=='pypi-compatible-wheel-v2':
                helper.configure_tooling(tooling)
                if helper.target_identity(tooling)!=identity['runtime'].get('target'):raise PythonUnsupported('native Python target changed after admission; audited rebind required')
            data={k:identity[k] for k in ('requirements','imports','tooling_requirements','input_key')}
            (fetch/'input.json').write_bytes(helper.canonical(data))
            # Restore only a root-sealed full lock; mutable partial resolver output
            # is never sufficient to alter a previously admitted resolution.
            if (stage/'lock.json').exists():shutil.copyfile(stage/'lock.json',fetch/'lock.json')
            elif (fetch/'lock.json').exists():(fetch/'lock.json').unlink()
            for item in (fetch,*fetch.rglob('*')):
                if item.is_symlink():raise ValueError('linked dependency staging path')
                if not item.is_dir():regular(item)
                os.chown(item,UID,GID)
            proof=stage/'proof';proof.mkdir(exist_ok=True);proof.chmod(0o700);os.chown(proof,UID,GID)
            run(['/usr/bin/mount','--make-rprivate','/'])
            run(['/usr/bin/mount','-t','tmpfs','-o','mode=0755,size=16m,nosuid,nodev','python-staging','/tmp'])
            sources={'python-fetch':fetch,'python-tooling':tooling,'python-proof':proof,'python-helper':stage/'helper.py',
                     'python-dependency.sock':job_path(job)/'python-dependency.sock'}
            if not stat.S_ISSOCK(sources['python-dependency.sock'].lstat().st_mode):raise ValueError('Python registry bridge must be a socket')
            for name,source in sources.items():
                target=Path('/tmp')/name
                if source.is_dir():target.mkdir()
                else:target.touch()
                run(['/usr/bin/mount','--bind',str(source),str(target)])
            python_sandbox_run(job,stage,'resolve')
            lock_info=regular(fetch/'lock.json')
            if lock_info.st_size>32*1024**2:raise PythonUnsupported('resolved lock exceeds inventory limit')
            lock_data=json.loads((fetch/'lock.json').read_text())
            if lock_data.get('input_key')!=identity['input_key']:raise ValueError('resolved lock input mismatch')
            if not 0<len(lock_data.get('wheels',[]))<=helper.MAX_DISTS or sum(row['bytes'] for row in lock_data['wheels'])>helper.MAX_DOWNLOAD:
                raise PythonUnsupported('resolved wheel budget exceeded')
            for row in lock_data['wheels']:
                helper.relative(row['filename'])
                if '/' in row['filename']:raise PythonUnsupported('invalid resolved wheel basename')
                if helper.wheel_info(fetch/'wheels'/row['filename'])!=row:raise ValueError('resolved wheel lock mismatch')
            if (stage/'lock.json').exists():
                if completion_json_large(stage/'lock.json')!=lock_data:raise ValueError('frozen resolution changed')
            else:completion_write(stage/'lock.json',lock_data)
            python_sandbox_run(job,stage,'install')
            verify_args=(fetch,tooling) if helper.POLICY=='pypi-compatible-wheel-v2' else (fetch,)
            before=helper.verify_install(*verify_args)
            browser_key=''
            if before['packages'].get('playwright'):
                browser_key=browser_select(before['packages']['playwright'])
                selected_browser=browser_runtime(browser_key,before['packages']['playwright'])
                browser_manifest=json.loads((selected_browser/'manifest.json').read_text())
                if digest_file(fetch/'site-packages/playwright/driver/package/browsers.json')!=browser_manifest['driver_declaration_sha256']:raise PythonUnsupported('Playwright driver browser declaration differs from attested runtime')
                target=Path('/tmp/python-browser');target.mkdir()
                run(['/usr/bin/mount','--bind',str(selected_browser),str(target)])
                python_sandbox_run(job,stage,'browser-probe',probe=True)
                browser_probe_receipt(proof/'browser.json',browser_key)
            python_sandbox_run(job,stage,'probe',probe=True)
            after=helper.verify_install(*verify_args)
            if before!=after:raise ValueError('probe changed immutable installation')
            python_probe_receipt(proof/'probe.json',identity,request)
            manifest=dict(after,schema_version=1,kind='python-project-runtime',input_key=identity['input_key'],runtime_digest=identity['runtime_digest'],checksum_verified=True,
                          proof_sha256=digest_file(proof/'probe.json'),policy=helper.POLICY)
            if browser_key:manifest.update(browser_key=browser_key,browser_proof_sha256=digest_file(proof/'browser.json'))
            key=hashlib.sha256(helper.canonical(manifest)).hexdigest();manifest['key']=key
            pending=cache/('.pending-'+job)
            if pending.exists():shutil.rmtree(pending)
            pending.mkdir();shutil.copytree(fetch/'site-packages',pending/'site-packages')
            (pending/'manifest.json').write_bytes(helper.canonical(manifest))
            file_modes={row['path']:row.get('mode',0o444) for row in manifest['files']}
            for item in (pending,*pending.rglob('*')):
                mode=0o555 if item.is_dir() else (file_modes[item.relative_to(pending/'site-packages').as_posix()] if item!=pending/'manifest.json' else 0o444)
                if mode not in (0o444,0o555):raise ValueError('unsafe Python published file mode')
                os.chown(item,0,0);item.chmod(mode)
            bundle=cache/key
            if bundle.exists():shutil.rmtree(pending)
            else:os.rename(pending,bundle)
            receipt.update(state='verified',bundle_key=key,lock_sha256=manifest['lock_sha256'],proof_sha256=manifest['proof_sha256'],verified_at=time.time())
            if browser_key:receipt['browser_key']=browser_key
            completion_write(stage/'receipt.json',receipt)
            python_project_bundle(job)
            # Publish the replay identity and fixed helper before the index that
            # makes this environment reusable by another assignment.
            identity_path=cache/(identity['input_key']+'.identity.json')
            helper_path=cache/(identity['input_key']+'.helper.py')
            if identity_path.exists():
                if completion_json(identity_path)!=identity:raise ValueError('cached Python identity changed')
            else:completion_write(identity_path,identity)
            if not helper_path.exists():
                helper_pending=cache/('.helper-'+job)
                if helper_pending.exists():regular(helper_pending);helper_pending.unlink()
                with helper_pending.open('xb') as out:
                    out.write((stage/'helper.py').read_bytes());out.flush();os.fsync(out.fileno())
                os.chown(helper_pending,0,0);helper_pending.chmod(0o444)
                os.replace(helper_pending,helper_path)
            if digest_file(helper_path)!=identity['runtime']['helper_sha256']:raise ValueError('cached Python helper differs')
            completion_write(index,{k:receipt[k] for k in ('bundle_key','lock_sha256','proof_sha256','browser_key') if k in receipt})
        except Exception as exc:
            unsupported=isinstance(exc,(helper.Unsupported,PythonUnsupported))
            receipt.update(state='unavailable' if unsupported else 'waiting',unsupported=unsupported,reason=str(exc)[:400],retry_at=time.time()+900)
            if getattr(exc,'diagnostic',None):receipt['diagnostic']=exc.diagnostic
            completion_write(stage/'receipt.json',receipt)
    return 0 if receipt.get('state')=='verified' else 1


def completion_json_large(path):
    info=regular(path)
    if info.st_uid!=os.geteuid() or info.st_mode&0o022 or info.st_size>32*1024**2:raise ValueError('unsafe dependency lock')
    return json.loads(path.read_text())


# Go test runtimes are data-only immutable snapshots. No package/toolchain code
# executes in this root supervisor; compilation happens after namespace isolation.
GO_RUNTIME_FIELDS = ('go_dependency_key', 'go_bundle_digest', 'go_toolchain_digest')
GO_TOOLCHAIN_SOURCE = Path('/usr/local/go')

def go_runtime_dir(path):
    path.mkdir(mode=0o755, parents=True, exist_ok=True)
    info=path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid!=os.geteuid() or info.st_mode&0o022:
        raise ValueError('unsafe Go runtime directory')
    return path

def go_runtime_inventory(root, frozen=False):
    result=[];total=0
    for base,dirs,files in os.walk(root,followlinks=False):
        for name in sorted(dirs+files):
            p=Path(base)/name;info=p.lstat();rel=p.relative_to(root).as_posix()
            if info.st_uid!=os.geteuid() or info.st_mode&0o022 or info.st_mode&0o7000:
                raise ValueError('untrusted Go runtime permissions')
            if stat.S_ISDIR(info.st_mode):
                if frozen and stat.S_IMODE(info.st_mode)!=0o555:raise ValueError('Go runtime directory mode changed')
                result.append({'path':rel,'kind':'directory','mode':0o555})
            elif stat.S_ISREG(info.st_mode) and info.st_nlink==1:
                total+=info.st_size
                if info.st_size>512*1024**2 or total>3*1024**3:raise ValueError('Go runtime exceeds snapshot bound')
                mode=0o555 if info.st_mode&0o111 else 0o444
                if frozen and stat.S_IMODE(info.st_mode)!=mode:raise ValueError('Go runtime file mode changed')
                result.append({'path':rel,'kind':'file','size':info.st_size,'mode':mode,'sha256':digest_file(p)})
            else:raise ValueError('Go runtime links and special files refused')
            if len(result)>100000:raise ValueError('Go runtime inventory exceeds entry bound')
    return sorted(result,key=lambda v:v['path'])

def go_runtime_manifest(kind,rows):
    return {'schema_version':1,'kind':kind,'files':rows}

def go_runtime_digest(value):
    return hashlib.sha256(json.dumps(value,sort_keys=True,separators=(',',':')).encode()).hexdigest()

def go_runtime_component(kind,digest):
    if not isinstance(digest,str) or not re.fullmatch('[0-9a-f]{64}',digest):raise ValueError('invalid Go runtime identity')
    base=DEPENDENCIES/'go-test'/kind;root=base/digest
    for p in (DEPENDENCIES,DEPENDENCIES/'go-test',base,root,root/'payload'):
        info=p.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid!=os.geteuid() or info.st_mode&0o022:raise ValueError('unsafe Go runtime snapshot')
    manifest=root/'manifest.json';info=regular(manifest)
    if info.st_uid!=os.geteuid() or info.st_mode&0o222 or info.st_size>32*1024**2:raise ValueError('unsafe Go runtime manifest')
    value=json.loads(manifest.read_text())
    if value.get('kind')!=kind or go_runtime_digest(value)!=digest:raise ValueError('Go runtime manifest identity changed')
    if go_runtime_inventory(root/'payload',True)!=value.get('files'):raise ValueError('Go runtime content changed')
    return root/'payload'

def go_runtime_snapshot(kind,source):
    import tempfile
    info=source.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid!=os.geteuid() or info.st_mode&0o022:raise ValueError('unsafe Go runtime source')
    value=go_runtime_manifest(kind,go_runtime_inventory(source));digest=go_runtime_digest(value)
    base=go_runtime_dir(DEPENDENCIES/'go-test'/kind);final=base/digest
    if final.exists():go_runtime_component(kind,digest);return digest
    stage=Path(tempfile.mkdtemp(prefix='.capture-',dir=base))
    try:
        payload=stage/'payload';payload.mkdir()
        for row in value['files']:
            p=payload/row['path']
            if row['kind']=='directory':p.mkdir()
            else:
                p.parent.mkdir(parents=True,exist_ok=True)
                with (source/row['path']).open('rb') as src,p.open('xb') as dst:
                    shutil.copyfileobj(src,dst);dst.flush();os.fsync(dst.fileno())
                p.chmod(row['mode'])
        for p in sorted([payload,*payload.rglob('*')],key=lambda p:len(p.parts),reverse=True):
            if p.is_dir():
                p.chmod(0o555);fd=os.open(p,os.O_RDONLY|os.O_DIRECTORY)
                try:os.fsync(fd)
                finally:os.close(fd)
        if go_runtime_inventory(source)!=value['files'] or go_runtime_inventory(payload,True)!=value['files']:
            raise ValueError('Go runtime source changed during capture')
        manifest=stage/'manifest.json'
        with manifest.open('x') as f:json.dump(value,f,sort_keys=True,separators=(',',':'));f.flush();os.fsync(f.fileno())
        manifest.chmod(0o444);stage.chmod(0o555)
        fd=os.open(stage,os.O_RDONLY|os.O_DIRECTORY)
        try:os.fsync(fd)
        finally:os.close(fd)
        try:os.rename(stage,final)
        except OSError:
            if not final.exists():raise
            go_runtime_component(kind,digest)
        fd=os.open(base,os.O_RDONLY|os.O_DIRECTORY)
        try:os.fsync(fd)
        finally:os.close(fd)
    finally:
        if stage.exists():shutil.rmtree(stage)
    return digest

def go_runtime_lookup(selection):
    if any(not isinstance(selection.get(k),str) or not re.fullmatch('[0-9a-f]{64}',selection[k]) for k in GO_RUNTIME_FIELDS):raise ValueError('incomplete Go runtime selection')
    modules=go_runtime_component('modules',selection['go_bundle_digest'])
    toolchain=go_runtime_component('toolchains',selection['go_toolchain_digest'])
    # The module snapshot includes the original trusted bundle manifest.
    manifest=json.loads((modules/'manifest.json').read_text())
    if manifest.get('key')!=selection['go_dependency_key'] or manifest.get('checksum_verified') is not True:raise ValueError('Go snapshot dependency identity mismatch')
    return modules,toolchain

def go_runtime_capture(work,expected=None,dependency_key=None):
    key=dependency_key or go_dependency_key(work)
    if expected:
        go_runtime_lookup(expected);return {k:expected[k] for k in GO_RUNTIME_FIELDS}
    if key is None:return None
    bundle=go_dependency_bundle(work,key=key)
    if bundle is None:raise FileNotFoundError('exact Go dependency bundle unavailable')
    home=go_runtime_dir(DEPENDENCIES/'go-test');index=go_runtime_dir(home/'selections')
    with (index/(key+'.lock')).open('a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX)
        record=index/(key+'.json')
        if record.exists():
            value=completion_json(record);go_runtime_lookup(value);return {k:value[k] for k in GO_RUNTIME_FIELDS}
        value={'go_dependency_key':key,'go_bundle_digest':go_runtime_snapshot('modules',bundle),'go_toolchain_digest':go_runtime_snapshot('toolchains',GO_TOOLCHAIN_SOURCE)}
        completion_write(record,value);return value

def go_runtime_mount(modules,toolchain):
    return ['--ro-bind',str(modules/'mod'),'/opt/go-modules','--ro-bind',str(modules/'manifest.json'),'/opt/go-dependencies.json',
            '--ro-bind',str(toolchain),'/opt/go-toolchain','--ro-bind',str(toolchain),'/usr/local/go','--setenv','GOROOT','/opt/go-toolchain',
            '--setenv','GOMODCACHE','/opt/go-modules','--setenv','GOPATH','/tmp/go','--setenv','GOCACHE','/tmp/go-build',
            '--setenv','GOPROXY','off','--setenv','GOSUMDB','off','--setenv','GOTOOLCHAIN','local','--setenv','GOENV','off',
            '--setenv','GOWORK','off','--setenv','GOTELEMETRY','off','--setenv','GOMAXPROCS','2','--setenv','GOFLAGS','-p=2',
            '--setenv','PATH','/opt/go-toolchain/bin:/usr/bin:/bin']

def go_runtime_requirement(job):
    requirement=job_path(job)/'go-runtime-requirement.json'
    if not requirement.exists():return None
    info=regular(requirement)
    if info.st_uid not in (0,pwd.getpwnam('admin').pw_uid) or info.st_mode&0o077 or info.st_size>16384:raise ValueError('unsafe Go runtime requirement')
    expected=json.loads(requirement.read_text())
    if set(expected)!=set(GO_RUNTIME_FIELDS)|{'schema_version'} or expected['schema_version']!=1:raise ValueError('invalid Go runtime requirement')
    if any(not isinstance(expected[k],str) or not re.fullmatch('[0-9a-f]{64}',expected[k]) for k in GO_RUNTIME_FIELDS):raise ValueError('invalid Go runtime requirement identity')
    return expected

def go_worker_runtime(job,work):
    p=job_path(job);expected=go_runtime_requirement(job)
    try:
        selection=go_runtime_capture(work,expected)
        if selection is None:return None
        completion_write(p/'go-runtime.json',dict(selection,owner_job=job,schema_version=1,state='verified',scope='immutable Go runtime selected before model execution'))
        return go_runtime_lookup(selection)
    except (OSError,ValueError) as error:
        failure=dict(expected or {})
        if not failure.get('go_dependency_key'):
            try:failure['go_dependency_key']=go_dependency_key(work)
            except (OSError,ValueError):pass
        completion_write(p/'go-runtime.json',dict(failure,owner_job=job,schema_version=1,state='unavailable',executed=False,reason=str(error)[:500],diagnostic='missing' if isinstance(error,FileNotFoundError) else 'integrity'))
        raise


def go_probe_paths(job,key,source_sha):
    if any(not isinstance(x,str) or not re.fullmatch('[0-9a-f]{64}',x) for x in (key,source_sha)):raise ValueError('invalid Go experiment identity')
    parent=job_path(job)/'go-probe-runtime';go_runtime_dir(parent)
    stage=go_runtime_dir(parent/key)
    intent={'owner_job':job,'go_dependency_key':key,'source_archive_sha256':source_sha}
    expected=go_runtime_requirement(job)
    if expected:
        if expected['go_dependency_key']!=key:raise ValueError('Go experiment expected dependency mismatch')
        intent['expected_runtime']=expected
    if (stage/'intent.json').exists():
        if completion_json(stage/'intent.json')!=intent:raise ValueError('Go experiment source changed')
    else:completion_write(stage/'intent.json',intent)
    return stage,intent

def go_probe_source_key(source,key):
    source_key=go_dependency_key(source)
    if source_key!=key:
        original=go_dependency_bundle(source,key=key)
        if original is None:raise FileNotFoundError('selected historical Go dependency bundle unavailable')
        metadata=json.loads((original/'manifest.json').read_text())
        if not (source/'go.mod').exists() or metadata.get('go_mod_sha256')!=digest_file(source/'go.mod'):
            raise ValueError('historical Go module declaration changed; selected environment needs a new admitted prerequisite')
    return source_key

def go_probe_unit(job,key):return 'lectern-go-runtime-'+job+'-'+key[:16]+'.service'

def go_probe_runtime(job,key,source_sha,generation,stop=False,execute=False):
    if type(generation) is not int or generation<1:raise ValueError('Go experiment generation missing')
    stage,intent=go_probe_paths(job,key,source_sha);name=go_probe_unit(job,key)
    value=dict(intent,schema_version=1,generation=generation,provenance='new_experiment')
    authority=stage/'authority.json'
    if execute:
        group=Path('/proc/self/cgroup').read_text().strip().split('::')[-1]
        if not group.endswith('/'+name):raise ValueError('Go experiment outside owned unit')
        current=completion_json(authority)
        if current.get('revoked',0)>=generation or current.get('generation')!=generation:return 1
        try:
            with ARTIFACT_LOCK.open('a') as guard:
                fcntl.flock(guard,fcntl.LOCK_EX);completion_capacity()
                source=stage/'source'
                if source.exists():shutil.rmtree(source)
                if evidence_extract_archive(job,source)!=source_sha:raise ValueError('Go experiment archived source differs')
            source_key=go_dependency_key(source) if intent.get('expected_runtime') else go_probe_source_key(source,key)
            selection=go_runtime_capture(source,expected=intent.get('expected_runtime'),dependency_key=key)
            value['source_input_key']=source_key
            if completion_json(authority).get('revoked',0)>=generation:return 1
            completion_write(stage/'receipt.json',dict(value,**selection,state='verified',scope='new isolated experiment environment; no claim of historical source use'))
            return 0
        except (OSError,ValueError) as error:
            completion_write(stage/'receipt.json',dict(value,state='unavailable',executed=False,retry_at=time.time()+120,reason=str(error)[:500],diagnostic='missing' if isinstance(error,FileNotFoundError) else 'integrity'));return 1
    with (stage/'guard').open('a') as guard:
        try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:return dict(value,state='stopping' if stop else 'recovering')
        current=completion_json(authority) if authority.exists() else {'generation':0,'revoked':0}
        if stop:
            current['revoked']=max(current['revoked'],generation);completion_write(authority,current)
            if current['generation']<=generation and completion_service_active(name):run(['/usr/bin/systemctl','stop','--no-block',name],timeout=3)
            return dict(value,state='stopping' if current['generation']<=generation and completion_service_active(name) else 'stopped')
        if current['revoked']>=generation:return dict(value,state='cancelled',executed=False)
        if generation<current['generation']:return dict(value,state='cancelled',executed=False)
        old=completion_json(stage/'receipt.json') if (stage/'receipt.json').exists() else None
        if old and old.get('state')=='verified':return dict(old,generation=generation)
        if old and old.get('state')=='unavailable' and time.time()<old.get('retry_at',0):return dict(old,generation=generation)
        if completion_service_active(name):return dict(value,state='recovering')
        if old:
            history=go_runtime_dir(stage/'history');saved=history/(go_runtime_digest(old)+'.json')
            if not saved.exists():completion_write(saved,old)
        current['generation']=generation;completion_write(authority,current)
        # Fixed supervisor executable frozen before the first asynchronous effect.
        frozen=stage/'runner.py';entry=stage/'entry.py'
        if not frozen.exists():
            raw=Path(__file__).read_bytes();write_new(frozen,raw.decode(),0o400)
            completion_write(stage/'executable.json',{'sha256':hashlib.sha256(raw).hexdigest()})
        if digest_file(frozen)!=completion_json(stage/'executable.json')['sha256']:raise ValueError('Go experiment executable changed')
        if not entry.exists():
            source="import importlib.util,sys\nfrom pathlib import Path\np=Path(__file__).parent\ns=importlib.util.spec_from_file_location('runner',p/'runner.py');r=importlib.util.module_from_spec(s);s.loader.exec_module(r)\n"
            for var in ('ROOT','DEPENDENCIES','ARTIFACT_LOCK','GO_TOOLCHAIN_SOURCE'):
                source+='r.'+var+'=Path('+repr(str(globals()[var]))+')\n'
            source+='sys.exit(r.main())\n';write_new(entry,source,0o400)
        cmd=['/usr/bin/systemd-run','--quiet','--unit='+name,'--property=RuntimeMaxSec=180','--property=MemoryMax=2G','--property=CPUQuota=200%','--property=TasksMax=32','--property=KillMode=control-group','--property=NoNewPrivileges=yes','--property=IPAddressDeny=any','--property=UMask=0077','/usr/bin/python3',str(entry),'_go-runtime','--job',job,'--dependency-key',key,'--source-sha256',source_sha,'--generation',str(generation)]
        run(cmd,pass_fds=(guard.fileno(),),timeout=3)
        return dict(value,state='recovering')


def go_dependency_key(work):
    parts = []
    for name in ('go.mod', 'go.sum'):
        path = work / name
        if not path.exists() and not path.is_symlink():
            if name == 'go.mod': return None
            parts.append(b'go.sum-absent\0')
            continue
        st = regular(path)
        if st.st_size > 4 * 1024**2:
            raise ValueError('dependency input too large')
        parts.append(name.encode() + b'\0' + path.read_bytes())
    return hashlib.sha256(b'\0'.join(parts)).hexdigest()


def go_dependency_bundle(work,key=None):
    key = key or go_dependency_key(work)
    if key is None:
        return None
    bundle = DEPENDENCIES / 'go' / key
    if not bundle.exists() and not bundle.is_symlink():
        return None
    # Only trusted fixed tooling or an administrator provisions bundles. No host module cache or
    # worker-selected path is exposed; immutable content is verified at install.
    for path in (DEPENDENCIES, DEPENDENCIES / 'go', bundle, bundle / 'mod'):
        st = path.lstat()
        if not stat.S_ISDIR(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o022:
            raise ValueError('unsafe dependency bundle directory')
    manifest = bundle / 'manifest.json'
    st = regular(manifest)
    if st.st_uid != 0 or st.st_mode & 0o222 or st.st_size > 65536:
        raise ValueError('unsafe dependency manifest')
    data = json.loads(manifest.read_text())
    if data.get('key') != key or data.get('checksum_verified') is not True:
        raise ValueError('dependency bundle provenance mismatch')
    return bundle



# This is trusted, fixed tooling, not a model-selected command. Only module
# metadata enters its filesystem; source code and host credentials never do.
DEPENDENCY_FETCH = r"""
import json, os, subprocess, time
from pathlib import Path
proxy = subprocess.Popen(['/usr/bin/socat', 'TCP4-LISTEN:18080,bind=127.0.0.1,reuseaddr,fork', 'UNIX-CONNECT:/dependency.sock'])
try:
    time.sleep(.2)
    go = '/usr/local/go/bin/go'
    meta = json.loads(subprocess.check_output([go, 'mod', 'edit', '-json'], text=True))
    for entry in meta.get('Replace') or []:
        if not entry['New'].get('Version'):
            raise RuntimeError('local replacement requires a separate source prerequisite')
    Path('/fetch/mod').mkdir(exist_ok=True)
    subprocess.run([go, 'mod', 'download', 'all'], check=True)
    subprocess.run([go, 'mod', 'verify'], check=True)
    Path('/fetch/verified.json').write_text(json.dumps({'module': meta['Module']['Path'],
        'go_version': subprocess.check_output([go, 'version'], text=True).strip()}))
finally:
    proxy.terminate()
"""


def dependency_state_path(key):
    return DEPENDENCIES / ('recovery-' + key)


def dependency_receipt(stage, value):
    target = stage / 'receipt.json'
    tmp = stage / 'receipt.tmp'
    with tmp.open('w') as out:
        json.dump(value, out)
        out.flush(); os.fsync(out.fileno())
    tmp.chmod(0o644)
    os.replace(tmp, target)


def dependency_status(job):
    p = job_path(job)
    # Never inspect changing inputs or prepare dependencies for a running agent.
    if status(job)['state'] == 'running':
        raise ValueError('dependency preflight requires a stopped worker')
    work = ensure_work(p)
    key = go_dependency_key(work)
    if key is None:
        return {'state': 'not_applicable', 'capability': 'go_modules'}
    if go_dependency_bundle(work) is not None:
        return {'state': 'verified', 'capability': 'go_modules', 'key': key}
    for directory in (DEPENDENCIES, DEPENDENCIES / 'go'):
        if not directory.exists():
            directory.mkdir(mode=0o755); directory.chmod(0o755)
    for path in (DEPENDENCIES, DEPENDENCIES / 'go'):
        st = path.lstat()
        if not stat.S_ISDIR(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o022:
            raise ValueError('unsafe dependency recovery directory')
    stage = dependency_state_path(key)
    stage.mkdir(mode=0o755, exist_ok=True)
    st = stage.lstat()
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o022:
        raise ValueError('unsafe dependency recovery stage')
    (stage / 'heartbeat').touch()
    # Serialize starts across controller retries and exact-input consumers.
    with (stage / 'lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        receipt = json.loads((stage / 'receipt.json').read_text()) if (stage / 'receipt.json').exists() else {}
        name = 'lectern-dependencies-' + key + '.service'
        active = subprocess.run(['/usr/bin/systemctl', 'is-active', name], capture_output=True, text=True).stdout.strip()
        if active in ('active', 'activating', 'deactivating'):
            return dict(receipt, state='recovering')
        if receipt.get('state') == 'recovering':
            receipt.update(state='failed', reason='provisioner exited before verification', retry_at=time.time()+900)
            dependency_receipt(stage, receipt)
        # A fresh installed toolchain is a verified environment change. A
        # timed cooldown permits network outages to recover without a human;
        # neither path reruns model work or declares the dependency fixed.
        environment = digest_file(Path('/usr/local/go/bin/go'))
        if receipt.get('attempts', 0) >= 3:
            if receipt.get('environment') == environment and time.time()-receipt.get('started_at',0) < 6*3600:
                return dict(receipt, state='unavailable')
            receipt['attempts'] = 0
            receipt['retry_at'] = 0
        if receipt.get('retry_at', 0) > time.time():
            return dict(receipt, state='waiting')
        if not storage_status()['ready'] or shutil.disk_usage(ROOT).free < 24*1024**3 or allocated_storage() > STORAGE_LIMIT-4*1024**3:
            return {'state': 'unavailable', 'capability': 'go_modules', 'key': key,
                    'reason': 'dependency recovery needs 4 GiB headroom above the storage floor'}
        inputs = stage / 'inputs'
        inputs.mkdir(mode=0o755, exist_ok=True)
        for filename in ('go.mod', 'go.sum'):
            # The original exact bytes key the bundle, even if Go adds sums in
            # its disposable staging directory. Never rewrite the source tree.
            if not (work / filename).exists(): continue
            data = (work / filename).read_bytes()
            dest = inputs / filename
            if dest.exists() and dest.read_bytes() != data:
                raise ValueError('dependency input changed since admission')
            dest.write_bytes(data); dest.chmod(0o444)
        if go_dependency_key(inputs) != key:
            raise ValueError('dependency input changed while reading')
        receipt = {'state': 'recovering', 'capability': 'go_modules', 'key': key,
                   'source_job': job, 'attempts': receipt.get('attempts', 0)+1,
                   'started_at': time.time(), 'environment': environment, 'requirement': 'verified offline Go module bundle for exact source inputs'}
        dependency_receipt(stage, receipt)
        command = ['/usr/bin/systemd-run', '--quiet', '--collect', '--unit='+name,
                   '--property=RuntimeMaxSec=600', '--property=MemoryMax=2G',
                   '--property=MemorySwapMax=0', '--property=CPUQuota=200%',
                   '--property=TasksMax=128', '--property=KillMode=control-group',
                   '--property=PrivateMounts=yes', '--property=UMask=0077',
                   INSTALL, '_dependencies', '--job', job]
        run(command)
        return receipt


def dependency_volume(stage):
    image = stage / 'work.ext4'
    (stage / 'work').mkdir(exist_ok=True)
    if image.exists():
        return image
    # A kill during formatting leaves only an unpublished disposable image.
    # Never mistake it for a successfully initialized recovery volume.
    temporary = stage / ('.initializing-' + str(uuid.uuid4()))
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        os.ftruncate(fd, 2*1024**3)
        run(['/usr/sbin/mkfs.ext4', '-q', '-F', str(temporary)])
        os.fsync(fd)
    finally:
        os.close(fd)
    os.rename(temporary, image)
    directory = os.open(stage, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(directory)
    finally: os.close(directory)
    return image


def dependency_execute(job):
    # Reserve one provisioner for the whole controller. The 4 GiB headroom
    # accounts for its bounded image plus final cache, not N concurrent copies.
    key = go_dependency_key(ensure_work(job_path(job)))
    stage = dependency_state_path(key)
    with (DEPENDENCIES / '.provision.lock').open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            receipt = json.loads((stage / 'receipt.json').read_text())
            receipt.update(state='waiting', reason='another prerequisite is being provisioned', retry_at=time.time()+30,
                           attempts=max(0,receipt.get('attempts',1)-1))
            dependency_receipt(stage,receipt)
            return 0
        return dependency_execute_locked(job)


def dependency_execute_locked(job):
    p = job_path(job)
    key = go_dependency_key(ensure_work(p))
    current = Path('/proc/self/cgroup').read_text().strip().split('::')[-1]
    if key is None or not current.endswith('/lectern-dependencies-' + key + '.service'):
        raise ValueError('dependency execution outside matching cgroup')
    stage = dependency_state_path(key)
    receipt = json.loads((stage / 'receipt.json').read_text())
    try:
        # A separate bounded disk, never the agent's workspace or a host cache.
        dependency_volume(stage)
        fetch = ensure_work(stage)
        for filename in ('go.mod', 'go.sum'):
            if (stage / 'inputs' / filename).exists():
                shutil.copyfile(stage / 'inputs' / filename, fetch / filename)
        for path in (fetch, fetch / 'go.mod', fetch / 'go.sum'):
            if path.exists(): os.chown(path, UID, GID)
        # Stage protected paths for the unprivileged bwrap process in this
        # service's private mount namespace, as for ordinary workshop workers.
        run(['/usr/bin/mount', '--make-rprivate', '/'])
        run(['/usr/bin/mount', '-t', 'tmpfs', '-o', 'mode=0755,size=16m,nosuid,nodev', 'dependency-staging', '/tmp'])
        Path('/tmp/fetch').mkdir()
        run(['/usr/bin/mount', '--bind', str(fetch), '/tmp/fetch'])
        Path('/tmp/dependency.sock').touch()
        sock = p / 'dependency.sock'
        if not stat.S_ISSOCK(sock.lstat().st_mode):
            raise ValueError('dependency bridge must be a socket')
        run(['/usr/bin/mount', '--bind', str(sock), '/tmp/dependency.sock'])
        cmd = ['/usr/bin/bwrap', '--die-with-parent', '--new-session', '--unshare-all', '--cap-drop', 'ALL',
               '--clearenv', '--tmpfs', '/', '--ro-bind', '/usr', '/usr']
        for path in ('/lib', '/lib64', '/bin'):
            if Path(path).exists(): cmd += ['--ro-bind', path, path]
        cmd += ['--proc', '/proc', '--dev', '/dev', '--tmpfs', '/tmp', '--dir', '/etc',
                '--bind', '/tmp/fetch', '/fetch', '--ro-bind', '/tmp/dependency.sock', '/dependency.sock',
                '--chdir', '/fetch']
        for name, value in {'HOME':'/tmp', 'PATH':'/usr/local/go/bin:/usr/bin:/bin',
                'GOMODCACHE':'/fetch/mod', 'GOCACHE':'/tmp/build', 'GOPATH':'/fetch/gopath',
                'GOPROXY':'http://127.0.0.1:18080', 'GOSUMDB':'sum.golang.org',
                'GONOSUMDB':'', 'GONOPROXY':'', 'GOPRIVATE':'', 'GOVCS':'*:off',
                'GOTOOLCHAIN':'local', 'GOENV':'off', 'GOWORK':'off', 'GOTELEMETRY':'off'}.items():
            cmd += ['--setenv', name, value]
        cmd += ['--', '/usr/bin/python3', '-c', DEPENDENCY_FETCH]
        def drop():
            resource.setrlimit(resource.RLIMIT_FSIZE, (256*1024**2,256*1024**2))
            os.setgroups([]); os.setgid(GID); os.setuid(UID)
        with (stage / 'fetch.log').open('w') as log:
            result = subprocess.Popen(cmd, preexec_fn=drop, close_fds=True, stdout=log, stderr=log)
            deadline = time.monotonic()+570
            while result.poll() is None:
                fresh = 0 <= time.time()-(stage/'heartbeat').stat().st_mtime < 60
                if not fresh or time.monotonic()>deadline:
                    raise RuntimeError('dependency controller heartbeat expired or provisioning timed out')
                time.sleep(1)
        if result.returncode:
            raise RuntimeError('offline module provisioning failed; retained fetch.log contains evidence')
        verified = json.loads((fetch / 'verified.json').read_text())
        # The trusted downloader has exited. Reject links and special files
        # before transferring its verified cache into an immutable bundle.
        paths = [fetch / 'mod', *(fetch / 'mod').rglob('*')]
        for path in paths:
            st = path.lstat()
            if not stat.S_ISDIR(st.st_mode): regular(path)
        bundle = DEPENDENCIES / 'go' / key
        pending = DEPENDENCIES / 'go' / ('.'+key)
        # Each installation attempt has a fresh controller-owned directory.
        # An interrupted copy is never confused with a verified bundle.
        pending = pending.with_name(pending.name + '-' + str(uuid.uuid4()))
        pending.mkdir(mode=0o755)
        shutil.copytree(fetch / 'mod', pending / 'mod')
        manifest = dict(verified, key=key, checksum_verified=True, source_job=job,
                        verification='go mod download all with public sumdb; go mod verify',
                        go_mod_sha256=digest_file(stage/'inputs/go.mod'), go_sum_sha256=digest_file(stage/'inputs/go.sum') if (stage/'inputs/go.sum').exists() else None)
        (pending / 'manifest.json').write_text(json.dumps(manifest))
        for path in [pending, *pending.rglob('*')]:
            os.chown(path, 0, 0); path.chmod(0o555 if path.is_dir() else 0o444)
        os.rename(pending, bundle)
        if go_dependency_bundle(stage / 'inputs') != bundle:
            raise ValueError('dependency bundle post-install verification failed')
        receipt.update(state='verified', verified_at=time.time(), evidence=manifest)
    except Exception as exc:
        receipt.update(state='failed', reason=str(exc), retry_at=time.time()+900)
    dependency_receipt(stage, receipt)
    return 0 if receipt['state'] == 'verified' else 1

def allocated_storage():
    # The image already accounts for mounted work. Include binaries, logs and
    # all other job data without double-counting that filesystem or hardlinks.
    total = 0
    seen = set()
    for current, dirs, files in (row for root in (ROOT, ASSET_CACHE, DEPENDENCIES, INTEGRATION_ROOT, SERVER_OBSERVATIONS_ROOT, SERVER_MAINTENANCE_ROOT, SERVER_MAINTENANCE_TOOLS) for row in os.walk(root, followlinks=False)):
        if Path(current).parent == ROOT:
            dirs[:] = [name for name in dirs if name != 'work']
        for name in dirs + files:
            st = (Path(current) / name).lstat()
            identity = (st.st_dev, st.st_ino)
            if identity not in seen:
                total += st.st_blocks * 512
                seen.add(identity)
    return total


def prepare(job):
    p = job_path(job)
    if shutil.disk_usage(ROOT).free < 20 * 1024**3:
        raise RuntimeError('less than 20 GiB backing-volume free space')
    allocated = allocated_storage()
    if allocated >= STORAGE_LIMIT:
        raise RuntimeError('retained job storage exceeds 200 GiB; archive before continuing')
    if p.exists():
        raise ValueError('job already exists; choose a new UUID')
    p.mkdir(mode=0o750)
    p.chmod(0o770)
    admin = pwd.getpwnam('admin')
    os.chown(p, admin.pw_uid, admin.pw_gid)
    image = p / 'work.ext4'
    fd = os.open(image, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    os.ftruncate(fd, 2 * 1024**3)
    os.close(fd)
    run(['/usr/sbin/mkfs.ext4', '-q', '-F', str(image)])
    work = p / 'work'
    work.mkdir()
    run(['/usr/bin/mount', '-o', 'loop,nosuid,nodev', str(image), str(work)])
    os.chown(work, admin.pw_uid, admin.pw_gid)
    # mkfs creates root-only lost+found; no user data exists at preparation.
    (work / 'lost+found').rmdir()
    return {'state': 'prepared', 'work': str(work), 'capacity_bytes': 2 * 1024**3}


def copy_job(job, source, review=False):
    destination = job_path(job)
    origin = job_path(source)
    if job == source or (destination / 'job.json').exists():
        raise ValueError('copy destination must be a fresh prepared job')
    if status(source)['state'] == 'running':
        raise ValueError('cannot copy a running job')
    dest = ensure_work(destination)
    src = ensure_work(origin)
    if review:
        # Never overlay reviewer edits onto the builder's original evidence.
        evidence = dest / '.lectern-review'
        if evidence.is_symlink() or (evidence.exists() and not evidence.is_dir()):
            raise ValueError('review evidence root must be a real directory')
        if (evidence / source).exists() or (evidence / source).is_symlink():
            raise ValueError('review evidence destination already exists')
        dest = evidence / source / 'work'
    elif any(dest.iterdir()):
        raise ValueError('copy destination must be empty')
    paths = sorted(src.rglob('*'), key=lambda item: len(item.parts))
    if len(paths) > 100000:
        raise ValueError('snapshot exceeds 100000 entries')
    total = 0
    for item in paths:
        kind = item.lstat().st_mode
        if stat.S_ISDIR(kind) or stat.S_ISLNK(kind):
            continue
        total += regular(item).st_size
    if total > 1900 * 1024**2:
        raise ValueError('snapshot exceeds 1900 MiB')
    if review:
        if shutil.disk_usage(ensure_work(destination)).free < total + 64 * 1024**2:
            raise ValueError('insufficient sandbox space for review evidence')
        dest.mkdir(parents=True)
        admin = pwd.getpwnam('admin')
        for folder in (evidence, evidence / source, dest):
            os.chown(folder, admin.pw_uid, admin.pw_gid)
    admin = pwd.getpwnam('admin')
    manifest = []
    for item in paths:
        rel = item.relative_to(src)
        if not review and rel == Path('autonomy-report.json'):
            continue
        target = dest / rel
        kind = item.lstat().st_mode
        if stat.S_ISLNK(kind):
            target.symlink_to(os.readlink(item))
        elif stat.S_ISDIR(kind):
            target.mkdir(exist_ok=True)
        else:
            # Source has completed and is unreachable to other sandbox jobs.
            with item.open('rb') as read, target.open('xb') as write:
                shutil.copyfileobj(read, write)
            target.chmod(item.stat().st_mode & 0o777)
        os.chown(target, admin.pw_uid, admin.pw_gid, follow_symlinks=False)
        if review:
            manifest.append({'path': str(rel), 'kind': 'symlink' if stat.S_ISLNK(kind) else 'directory' if stat.S_ISDIR(kind) else 'file',
                             'sha256': digest_file(target) if stat.S_ISREG(kind) else None,
                             'link': os.readlink(target) if stat.S_ISLNK(kind) else None})
    if review:
        receipt = evidence / source / 'manifest.json'
        receipt.write_text(json.dumps({'source_job': source, 'purpose': 'untrusted reviewer evidence, not approval', 'files': manifest}, indent=2))
        os.chown(receipt, admin.pw_uid, admin.pw_gid)
    else:
        # Do not populate the next worker's submission path with a stale report.
        # Preserve its exact bytes separately so inherited checksum evidence can
        # still be checked without rewriting the original manifest.
        prior = src / 'autonomy-report.json'
        if prior.exists() or prior.is_symlink():
            st = regular(prior)
            if st.st_size > 128 * 1024:
                raise ValueError('inherited report exceeds 128 KiB')
            reports = dest / '.lectern-reports'
            if reports.is_symlink() or (reports.exists() and not reports.is_dir()):
                raise ValueError('inherited report root must be a real directory')
            reports.mkdir(exist_ok=True)
            saved = reports / source
            if saved.exists() or saved.is_symlink():
                raise ValueError('inherited report destination already exists')
            saved.mkdir()
            data = prior.read_bytes()
            if len(data) > 128 * 1024:
                raise ValueError('inherited report grew past 128 KiB')
            report_copy = saved / 'autonomy-report.json'
            with report_copy.open('xb') as out:
                out.write(data)
            receipt = saved / 'manifest.json'
            receipt.write_text(json.dumps({'source_job': source,
                'purpose': 'untrusted prior report evidence, not current submission or approval',
                'original_path': 'autonomy-report.json',
                'preserved_path': str(report_copy.relative_to(dest)),
                'sha256': hashlib.sha256(data).hexdigest()}, indent=2))
            for entry in (reports, saved, report_copy, receipt):
                os.chown(entry, admin.pw_uid, admin.pw_gid)
    # mkdir honors the service umask. Preserve inherited directory permissions
    # only after population/report handoff, so read-only evidence stays exact.
    # lstat avoids following directory symlinks supplied by an isolated worker.
    for item in reversed(paths):
        info = item.lstat()
        if stat.S_ISDIR(info.st_mode):
            (dest / item.relative_to(src)).chmod(info.st_mode & 0o777)
    if not review:
        dest.chmod(src.lstat().st_mode & 0o777)
    return {'state': 'copied', 'entries': len(paths), 'bytes': total}


def report(job):
    p = job_path(job)
    if status(job)['state'] == 'running':
        raise ValueError('report is available only after the job stops')
    work = ensure_work(p)
    directory = os.open(work, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        fd = os.open('autonomy-report.json', os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
        try:
            st = os.fstat(fd)
            if not stat.S_ISREG(st.st_mode) or st.st_nlink != 1 or st.st_size > 128 * 1024:
                raise ValueError('report must be a regular singly-linked file at most 128 KiB')
            with os.fdopen(fd, 'rb', closefd=False) as stream:
                content = stream.read(128 * 1024 + 1)
            if len(content) > 128 * 1024:
                raise ValueError('report grew past 128 KiB')
            # Reject malformed JSON, but preserve the exact original bytes.
            json.loads(content)
            sys.stdout.buffer.write(content)
            sys.stdout.buffer.flush()
        finally:
            os.close(fd)
    finally:
        os.close(directory)


def archive(job):
    p = job_path(job)
    if status(job)['state'] == 'running':
        raise ValueError('archive is available only after the job stops')
    work = ensure_work(p)
    def safe_member(member):
        if not (member.isfile() or member.isdir() or member.issym() or member.islnk()):
            raise ValueError('special files cannot be exported')
        member.mode &= 0o777
        member.uid = member.gid = 0
        member.uname = member.gname = ''
        return member
    # dereference=False archives a symlink itself, never its host target.
    # Only work is reachable here: assets/credentials/logs are sibling paths.
    with tarfile.open(fileobj=sys.stdout.buffer, mode='w|gz', dereference=False) as out:
        out.add(work, arcname='work', recursive=True, filter=safe_member)



def snapshot_stage(job):
    stage = job_path(job) / 'artifact-state'
    stage.mkdir(mode=0o755, exist_ok=True)
    st = stage.lstat()
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.geteuid() or st.st_mode & 0o022:
        raise ValueError('unsafe artifact state directory')
    return stage


def snapshot(job):
    p = job_path(job)
    if status(job)['state'] == 'running':
        raise ValueError('artifact export requires a stopped worker')
    target = p / 'artifact.tar.gz'
    if target.exists():
        if regular(target).st_size <= 0:
            raise ValueError('empty artifact')
        return {'state': 'ready'}
    stage = snapshot_stage(job)
    with (stage / 'start.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        name = 'lectern-artifact-' + job + '.service'
        active = subprocess.run(['/usr/bin/systemctl', 'is-active', name], capture_output=True, text=True).stdout.strip()
        if active in ('active', 'activating', 'deactivating'):
            return {'state': 'exporting'}
        receipt = json.loads((stage / 'receipt.json').read_text()) if (stage / 'receipt.json').exists() else {}
        if receipt.get('state') == 'exporting':
            receipt.update(state='waiting', reason='export interrupted; original workspace retained', retry_at=time.time()+60)
            dependency_receipt(stage, receipt)
        if receipt.get('retry_at', 0) > time.time():
            return receipt
        capacity = storage_status()
        if not capacity['ready'] or capacity['free_bytes'] < 23*1024**3 or capacity['allocated_bytes'] > STORAGE_LIMIT-3*1024**3:
            return {'state': 'waiting', 'reason': 'artifact export needs 3 GiB storage headroom'}
        dependency_receipt(stage, {'state': 'exporting'})
        run(['/usr/bin/systemd-run', '--quiet', '--collect', '--unit='+name,
             '--property=RuntimeMaxSec=600', '--property=MemoryMax=512M',
             '--property=CPUQuota=100%', '--property=TasksMax=16',
             '--property=KillMode=control-group', '--property=UMask=0077',
             INSTALL, '_snapshot', '--job', job])
        return {'state': 'exporting'}


def snapshot_execute(job):
    p = job_path(job)
    stage = snapshot_stage(job)
    with ARTIFACT_LOCK.open('a') as global_lock, (stage / 'export.lock').open('a') as lock:
        fcntl.flock(global_lock, fcntl.LOCK_EX)
        fcntl.flock(lock, fcntl.LOCK_EX)
        if status(job)['state'] == 'running':
            raise ValueError('artifact export requires a stopped worker')
        target = p / 'artifact.tar.gz'
        if target.exists():
            regular(target)
            return 0
        # Only this fixed private temporary path is replaced after a crash.
        temp = stage / 'archive.tmp'
        try:
            capacity = storage_status()
            if not capacity['ready'] or capacity['free_bytes'] < 23*1024**3 or capacity['allocated_bytes'] > STORAGE_LIMIT-3*1024**3:
                raise RuntimeError('artifact export needs 3 GiB storage headroom')
            if temp.exists() or temp.is_symlink():
                regular(temp)
                temp.unlink()
            work = ensure_work(p)
            def safe_member(member):
                if not (member.isfile() or member.isdir() or member.issym() or member.islnk()):
                    raise ValueError('special files cannot be exported')
                member.mode &= 0o777
                member.uid = member.gid = 0
                member.uname = member.gname = ''
                return member
            with temp.open('xb') as raw:
                with gzip.GzipFile(fileobj=raw, mode='wb', compresslevel=1, mtime=0) as compressed:
                    with tarfile.open(fileobj=compressed, mode='w|', dereference=False) as out:
                        out.add(work, arcname='work', recursive=True, filter=safe_member)
                raw.flush(); os.fsync(raw.fileno())
            digest = digest_file(temp)
            os.chown(temp, 0, pwd.getpwnam('admin').pw_gid)
            temp.chmod(0o440)
            os.replace(temp, target)
            fd = os.open(p, os.O_DIRECTORY)
            try: os.fsync(fd)
            finally: os.close(fd)
            dependency_receipt(stage, {'state': 'ready', 'sha256': digest, 'bytes': target.stat().st_size})
            return 0
        except Exception as exc:
            dependency_receipt(stage, {'state': 'waiting', 'reason': str(exc)[:500], 'retry_at': time.time()+300})
            raise

# Documentary completion is a separate derived artifact. It never rewrites a
# builder's archive or retroactively grants approval to its rejected lineage.
OVERLAY_CLI = '/usr/local/bin/lectern'
COMPLETION_MAX_BYTES = 1900 * 1024**2


class CompletionOverlayViolation(ValueError):
    pass


def completion_stage(job, create=False):
    stage = job_path(job) / 'completion'
    if create:
        stage.mkdir(mode=0o700, exist_ok=True)
    st = stage.lstat()
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.geteuid() or st.st_mode & 0o077:
        raise ValueError('unsafe completion state directory')
    return stage


def completion_json(path):
    info = regular(path)
    if info.st_uid != os.geteuid() or info.st_mode & 0o022 or info.st_size > 256 * 1024:
        raise ValueError('unsafe completion receipt')
    return json.loads(path.read_text())


def completion_write(path, value):
    temp = path.with_suffix('.tmp')
    if temp.exists() or temp.is_symlink():
        regular(temp); temp.unlink()
    write_new(temp, json.dumps(value, sort_keys=True), 0o600)
    with temp.open('rb') as stream:
        os.fsync(stream.fileno())
    os.replace(temp, path)
    fd = os.open(path.parent, os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)


def completion_paths(source):
    root_info = source.lstat()
    if not stat.S_ISDIR(root_info.st_mode) or root_info.st_mode & 0o7000:
        raise ValueError('completion root must be an ordinary directory')
    paths = sorted(source.rglob('*'), key=lambda p: (len(p.parts), str(p)))
    if len(paths) > 100000:
        raise ValueError('completion snapshot exceeds 100000 entries')
    total = 0
    for item in paths:
        info = item.lstat()
        if info.st_mode & 0o7000:
            raise ValueError('completion special permission bits are forbidden')
        if stat.S_ISDIR(info.st_mode):
            continue
        total += regular(item).st_size
        if total > COMPLETION_MAX_BYTES:
            raise ValueError('completion snapshot exceeds 1900 MiB')
    return paths


def completion_copy(source, destination, omit_report=False):
    paths = completion_paths(source)
    if destination.exists():
        if destination.is_symlink() or not destination.is_dir() or any(destination.iterdir()):
            raise ValueError('completion copy destination must be absent or empty')
    else:
        destination.mkdir(mode=0o700)
    for item in paths:
        rel = item.relative_to(source)
        if omit_report and rel == Path('autonomy-report.json'):
            continue
        target = destination / rel
        if item.is_dir():
            target.mkdir(mode=0o700)
        else:
            source_info = regular(item)
            with item.open('rb') as src, target.open('xb') as dest:
                shutil.copyfileobj(src, dest)
            if target.stat().st_size != source_info.st_size:
                raise ValueError('completion input changed during copying')
            target.chmod(source_info.st_mode & 0o777)
    for item in reversed(paths):
        if item.is_dir():
            (destination / item.relative_to(source)).chmod(item.stat().st_mode & 0o777)
    destination.chmod(source.stat().st_mode & 0o777)


def completion_inspect(path):
    result = subprocess.run([OVERLAY_CLI, 'autonomy-overlay-inspect', str(path)], capture_output=True, text=True, timeout=300)
    if result.returncode == 2:
        raise ValueError('invalid completion tree: ' + result.stderr[-400:])
    if result.returncode:
        raise RuntimeError('completion tree inspector unavailable: ' + result.stderr[-400:])
    value = json.loads(result.stdout)
    digest = value.get('tree_sha256', '')
    if len(digest) != 64 or any(c not in '0123456789abcdef' for c in digest):
        raise RuntimeError('completion tree inspector returned invalid digest')
    return digest


def completion_binding(stage):
    try:
        binding = completion_json(stage / 'baseline.json')
        if binding.get('policy') != 'documentary-v1' or completion_inspect(stage / 'baseline') != binding.get('baseline_sha256'):
            raise ValueError('frozen completion baseline identity changed')
        job_path(binding['source_job'])
        return binding
    except (ValueError, KeyError) as exc:
        raise RuntimeError('trusted completion baseline unavailable: ' + str(exc)) from exc


def completion_extract_archive(source, destination, allow_stopped=False):
    origin = job_path(source)
    allowed_states = ('done', 'failed', 'stopped') if allow_stopped else ('done',)
    if status(source)['state'] not in allowed_states:
        raise ValueError('completion source must be an eligible stopped worker')
    archive = origin / 'artifact.tar.gz'
    try:
        expected_identity = archive_identity(source)['sha256']
    except ValueError as exc:
        raise RuntimeError('trusted archive receipt invalid') from exc
    digest = digest_file(archive)
    if expected_identity != digest:
        raise RuntimeError('completion source archive checksum mismatch')
    if destination.exists():
        if destination.is_symlink() or not destination.is_dir() or any(destination.iterdir()):
            raise ValueError('archive extraction destination must be empty')
    else:
        destination.mkdir(parents=True, mode=0o700)
    seen, directories, total = set(), [], 0
    with tarfile.open(archive, 'r:gz') as tar:
        for member in tar:
            parts = Path(member.name).parts
            if not parts or parts[0] != 'work' or member.name != '/'.join(parts) or '..' in parts or '\\' in member.name:
                raise CompletionOverlayViolation('invalid path in completion source archive')
            if member.name in seen or len(seen) >= 100000:
                raise CompletionOverlayViolation('duplicate or excessive completion archive entries')
            seen.add(member.name)
            if member.mode & ~0o777 or not (member.isdir() or member.isfile()):
                raise CompletionOverlayViolation('completion source archive contains links/special files or modes')
            target = destination.joinpath(*parts[1:])
            if len(parts) == 1:
                if not member.isdir():
                    raise CompletionOverlayViolation('archive root must be a directory')
            elif member.isdir():
                target.mkdir(mode=0o700)
            else:
                total += member.size
                if total > COMPLETION_MAX_BYTES or member.size < 0:
                    raise CompletionOverlayViolation('completion source archive exceeds size limit')
                with tar.extractfile(member) as src, target.open('xb') as out:
                    shutil.copyfileobj(src, out)
                if target.stat().st_size != member.size:
                    raise CompletionOverlayViolation('truncated completion source archive')
                target.chmod(member.mode)
            if member.isdir():
                directories.append((target, member.mode))
    if 'work' not in seen:
        raise CompletionOverlayViolation('completion archive lacks work root')
    for directory, mode in reversed(directories):
        directory.chmod(mode)
    if digest_file(archive) != digest:
        raise RuntimeError('completion source archive changed during extraction')
    return digest


def evidence_member_parts(name):
    parts=Path(name).parts
    if not parts or parts[0]!='work' or name!='/'.join(parts) or '..' in parts or '\\' in name or len(os.fsencode(name))>4096:
        raise ValueError('unsafe evidence archive member path')
    return parts


def evidence_extract_archive(source, destination):
    """Restore untrusted evidence without interpreting any archived link target.

    Symlinks are literal data. Hardlinks are expanded into independent regular
    files using only validated tar members, never filesystem link resolution.
    """
    if status(source)['state'] not in ('done','failed','stopped'):raise ValueError('archive evidence requires a terminal source')
    archive=job_path(source)/'artifact.tar.gz'
    expected=archive_identity(source)['sha256']
    return evidence_extract_verified_archive(archive,expected,destination)


def evidence_extract_verified_archive(archive,expected,destination):
    info=regular(archive)
    if info.st_uid!=os.geteuid() or info.st_mode&0o022 or not isinstance(expected,str) or len(expected)!=64 or any(c not in '0123456789abcdef' for c in expected):raise ValueError('unsafe immutable evidence archive')
    if digest_file(archive)!=expected:raise RuntimeError('evidence archive checksum mismatch')
    for parent in (destination,*destination.parents):
        if parent.is_symlink():raise ValueError('linked evidence destination ancestor')
    if destination.exists():
        if not destination.is_dir() or any(destination.iterdir()):raise ValueError('evidence extraction destination must be empty')
    else:destination.mkdir(parents=True,mode=0o700)
    with tarfile.open(archive,'r:gz') as tar:
        members={};metadata_bytes=0
        for member in tar:
            evidence_member_parts(member.name)
            metadata_bytes+=len(os.fsencode(member.name))+len(os.fsencode(member.linkname))
            if member.name in members or len(members)>=100000 or metadata_bytes>32*1024**2:
                raise ValueError('duplicate or excessive evidence archive metadata')
            if member.mode&~0o777 or not (member.isdir() or member.isfile() or member.issym() or member.islnk()):
                raise ValueError('evidence archive special file or permission bits')
            if member.size<0 or member.size>COMPLETION_MAX_BYTES:raise ValueError('invalid evidence file size')
            if not member.isfile() and member.size!=0:raise ValueError('unexpected evidence link/directory payload')
            if member.issym() and (not member.linkname or '\x00' in member.linkname or len(os.fsencode(member.linkname))>4096):
                raise ValueError('invalid evidence symlink text')
            if member.islnk():evidence_member_parts(member.linkname)
            members[member.name]=member
        if 'work' not in members or not members['work'].isdir():raise ValueError('evidence archive needs directory root')
        for name,member in members.items():
            parts=evidence_member_parts(name)
            for count in range(1,len(parts)):
                ancestor=members.get('/'.join(parts[:count]))
                if ancestor is None or not ancestor.isdir():raise ValueError('evidence member parent is not an archive directory')
        resolved={};total=0
        for name,member in members.items():
            if not (member.isfile() or member.islnk()):continue
            current=member;chain=set()
            while current.islnk():
                if current.name in resolved:
                    current=resolved[current.name];break
                if current.name in chain:raise ValueError('cyclic evidence hardlink')
                chain.add(current.name)
                if current.linkname not in members:raise ValueError('evidence hardlink target is absent')
                current=members[current.linkname]
            if not current.isfile():raise ValueError('evidence hardlink must terminate at archive regular contents')
            for linked in chain:resolved[linked]=current
            resolved[name]=current;total+=current.size
            if total>COMPLETION_MAX_BYTES:raise ValueError('expanded evidence archive exceeds 1900 MiB')
        # All ancestor relationships are known before the first archive write.
        # Open directory descriptors with NOFOLLOW for every data destination.
        root_fd=os.open(destination,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
        def parent_fd(name):
            fd=os.dup(root_fd)
            try:
                for part in evidence_member_parts(name)[1:-1]:
                    child=os.open(part,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=fd)
                    os.close(fd);fd=child
                return fd
            except BaseException:
                os.close(fd);raise
        try:
            for name,member in sorted(members.items(),key=lambda row:(len(Path(row[0]).parts),row[0])):
                if name=='work' or not member.isdir():continue
                fd=parent_fd(name)
                try:os.mkdir(Path(name).name,mode=0o700,dir_fd=fd)
                finally:os.close(fd)
            for name,content in sorted(resolved.items()):
                fd=parent_fd(name)
                try:
                    out_fd=os.open(Path(name).name,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600,dir_fd=fd)
                    with os.fdopen(out_fd,'wb') as out,tar.extractfile(content) as src:
                        shutil.copyfileobj(src,out)
                        if out.tell()!=content.size:raise ValueError('truncated evidence archive contents')
                        os.fchmod(out.fileno(),members[name].mode)
                finally:os.close(fd)
            # Create link objects LAST. Their text is never used in a host open,
            # chmod, chown, traversal, byte count, digest or file copy.
            for name,member in sorted(members.items()):
                if not member.issym():continue
                fd=parent_fd(name)
                try:os.symlink(member.linkname,Path(name).name,dir_fd=fd)
                finally:os.close(fd)
            for name,member in sorted(members.items(),key=lambda row:len(Path(row[0]).parts),reverse=True):
                if not member.isdir():continue
                if name=='work':os.fchmod(root_fd,member.mode);continue
                fd=parent_fd(name)
                try:
                    child=os.open(Path(name).name,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=fd)
                    try:os.fchmod(child,member.mode)
                    finally:os.close(child)
                finally:os.close(fd)
        finally:os.close(root_fd)
    if digest_file(archive)!=expected:raise RuntimeError('evidence archive changed during extraction')
    return expected


def evidence_inventory(root):
    """Hash regular bytes and link text separately; never follow a link."""
    info=root.lstat()
    if not stat.S_ISDIR(info.st_mode):raise ValueError('evidence root must be a directory')
    rows=[];total=0;stack=[root]
    while stack:
        directory=stack.pop()
        for item in sorted(directory.iterdir()):
            st=item.lstat();name=item.relative_to(root).as_posix()
            if len(rows)>=100000 or st.st_mode&0o7000:raise ValueError('invalid evidence inventory size or mode')
            row={'path':name,'mode':stat.S_IMODE(st.st_mode),'sha256':None,'link':None}
            if stat.S_ISLNK(st.st_mode):row.update(kind='symlink',link=os.readlink(item))
            elif stat.S_ISDIR(st.st_mode):row['kind']='directory';stack.append(item)
            else:
                regular(item);total+=st.st_size
                if total>COMPLETION_MAX_BYTES:raise ValueError('evidence inventory exceeds size limit')
                row.update(kind='file',size=st.st_size,sha256=digest_file(item))
            rows.append(row)
    return sorted(rows,key=lambda row:row['path'])


def evidence_tree_identity(root, scheme=None):
    rows=evidence_inventory(root)
    has_links=any(row['kind']=='symlink' for row in rows)
    if scheme is None:scheme='general-evidence-v1' if has_links else 'documentary-tree-v1'
    if scheme=='documentary-tree-v1':
        if has_links:raise ValueError('links added to legacy evidence tree')
        return scheme,completion_inspect(root),rows
    if scheme!='general-evidence-v1':raise ValueError('unknown evidence digest scheme')
    data={'scheme':scheme,'root_mode':stat.S_IMODE(root.lstat().st_mode),'files':rows}
    digest=hashlib.sha256(json.dumps(data,sort_keys=True,separators=(',',':')).encode()).hexdigest()
    return scheme,digest,rows


def copy_archive_work(job, source, preserve_report=False):
    destination=job_path(job)
    if job==source or (destination/'job.json').exists():raise ValueError('archive work needs fresh destination')
    stage=completion_stage(job,create=True)
    expected={'source_job':source,'source_archive_sha256':archive_identity(source)['sha256']}
    kind='archive-resume' if preserve_report else 'archive-work'
    intent=stage/(kind+'-intent.json');published=stage/(kind+'-ready.json')
    if intent.exists():
        if completion_json(intent)!=expected:raise ValueError('archive work source changed')
    else:
        if any(ensure_work(destination).iterdir()):raise ValueError('archive work destination must be empty before reservation')
        completion_write(intent,expected)
    if published.exists():return completion_json(published)
    work=ensure_work(destination)
    # Only a destination with our durable intent and no launched worker may be reset.
    for item in work.iterdir():
        if item.is_dir() and not item.is_symlink():shutil.rmtree(item)
        else:item.unlink()
    copied=stage/'archive-work-staging'
    if copied.exists():shutil.rmtree(copied)
    digest=evidence_extract_archive(source,copied)
    if digest!=expected['source_archive_sha256']:raise ValueError('archive source changed during extraction')
    scheme,tree,_=evidence_tree_identity(copied,scheme='general-evidence-v1')
    # Copy symlinks as data; copytree preserves directory and file modes.
    shutil.copytree(copied,work,symlinks=True,dirs_exist_ok=True)
    prior=work/'autonomy-report.json'
    if not preserve_report and (prior.exists() or prior.is_symlink()):
        if regular(prior).st_size>128*1024:raise ValueError('archived report transport exceeds limit')
        reports=work/'.lectern-reports'
        if reports.is_symlink() or (reports.exists() and not reports.is_dir()):raise ValueError('unsafe archived report handoff root')
        reports.mkdir(exist_ok=True)
        saved=reports/source
        saved.mkdir()
        data=prior.read_bytes();prior.rename(saved/'autonomy-report.json')
        (saved/'manifest.json').write_text(json.dumps(dict(source_job=source,source_archive_sha256=digest,original_path='autonomy-report.json',sha256=hashlib.sha256(data).hexdigest(),purpose='prior archived report evidence, not current submission or approval')))
    admin=pwd.getpwnam('admin')
    for item in (work,*work.rglob('*')):os.chown(item,admin.pw_uid,admin.pw_gid,follow_symlinks=False)
    receipt=dict(expected,state='copied',source_tree_sha256=tree,evidence_digest_scheme=scheme,transport_handoff='root report retained for same-task correction' if preserve_report else 'autonomy-report.json moved to .lectern-reports/<source_job> when present')
    completion_write(published,receipt)
    return receipt


def copy_archive_review(job, source, derived=False):
    """Give a plan auditor exact exported evidence rather than mutable work."""
    destination = job_path(job)
    job_path(source)
    if job == source or (destination / 'job.json').exists():
        raise ValueError('archive review evidence requires a fresh prepared job')
    stage = completion_stage(job, create=True)
    derived_receipt = completion_ready(completion_stage(source)) if derived else None
    identity = derived_receipt['derived_archive_sha256'] if derived else archive_identity(source)['sha256']
    expected = {'source_job': source, 'source_archive_sha256': identity}
    if derived: expected.update(derived_archive_sha256=identity, source_kind='verified_documentary_derived')
    evidence_id = source + ('-derived' if derived else '')
    intent = stage / ('archive-review-' + evidence_id + '-intent.json')
    published = stage / ('archive-review-' + evidence_id + '-ready.json')
    with intent.with_suffix('.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        work = ensure_work(destination)
        evidence_root = work / '.lectern-review'
        if evidence_root.is_symlink() or (evidence_root.exists() and not evidence_root.is_dir()):
            raise ValueError('unsafe archive review evidence root')
        evidence = evidence_root / evidence_id
        if published.exists():
            receipt = completion_json(published)
            if any(receipt.get(key) != value for key,value in expected.items()):
                raise RuntimeError('archive review source identity changed')
            _,actual,_=evidence_tree_identity(evidence/'work',receipt.get('evidence_digest_scheme','documentary-tree-v1'))
            if actual != receipt.get('evidence_tree_sha256'):
                raise RuntimeError('published archived review evidence changed')
            return receipt
        if intent.exists():
            if completion_json(intent) != expected:
                raise RuntimeError('partial archive review source identity changed')
            if evidence.is_symlink():
                raise RuntimeError('linked partial archive review evidence')
            if evidence.exists():
                shutil.rmtree(evidence)
        else:
            if evidence.exists() or evidence.is_symlink():
                raise ValueError('archive review evidence already exists')
            completion_write(intent, expected)
        copied = evidence / 'work'
        if derived:
            evidence.mkdir(parents=True, mode=0o700)
            completion_copy(completion_stage(source) / 'derived/work', copied)
            if completion_inspect(copied) != derived_receipt['derived_tree_sha256']:
                raise RuntimeError('derived audit evidence tree differs')
            digest = identity
        else:
            digest = evidence_extract_archive(source, copied)
        if digest != identity:
            raise RuntimeError('archive changed during review copy')
        scheme,tree_hash,manifest=evidence_tree_identity(copied)
        (evidence / 'manifest.json').write_text(json.dumps({**expected, 'source_job': source,
            'source_archive_sha256': digest, 'purpose': 'untrusted archived evidence, not approval; symlink targets are literal untrusted text',
            'evidence_digest_scheme':scheme,'evidence_tree_sha256':tree_hash,'files': manifest}, indent=2))
        admin = pwd.getpwnam('admin')
        for item in (evidence_root, evidence, *evidence.rglob('*')):
            os.chown(item, admin.pw_uid, admin.pw_gid, follow_symlinks=False)
        receipt = dict(expected, state='copied', evidence_tree_sha256=tree_hash, evidence_digest_scheme=scheme)
        completion_write(published, receipt)
        return receipt


def archive_report_unit(job):
    job_path(job)
    return 'lectern-archive-report-'+job+'.service'


def archive_report_cached(stage, digest):
    path=stage/'report.json'
    if not path.exists():return None
    info=regular(path)
    if info.st_uid!=os.geteuid() or info.st_mode&0o022 or info.st_size>1024*1024:
        raise RuntimeError('unsafe archived report receipt')
    value=json.loads(path.read_text())
    if value.get('archive_sha256')!=digest:raise RuntimeError('archived report identity changed')
    if value.get('state')=='ready':
        if not isinstance(value.get('report'),str):raise RuntimeError('invalid cached archived report text')
        raw=value['report'].encode('utf-8')
        if len(raw)>128*1024 or hashlib.sha256(raw).hexdigest()!=value.get('report_sha256'):
            raise RuntimeError('cached archived report bytes changed')
    return value


def archive_report(job):
    p=job_path(job)
    if not (p/'artifact.tar.gz').exists() and not (p/'artifact.tar.gz').is_symlink():
        return {'state':'exporting','reason':'source immutable export is not ready'}
    digest=archive_identity(job)['sha256'];stage=snapshot_stage(job)
    cancelled=(stage/'report-cancel.json').read_bytes() if (stage/'report-cancel.json').exists() else b''
    with (stage/'report-start.lock').open('a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX)
        previous=archive_report_cached(stage,digest)
        if previous and previous.get('state') in ('ready','unavailable'):return previous
        if cancelled!=((stage/'report-cancel.json').read_bytes() if (stage/'report-cancel.json').exists() else b''):
            return {'state':'waiting','archive_sha256':digest,'reason':'archive report read cancelled'}
        if completion_service_active(archive_report_unit(job)):
            return {'state':'exporting','archive_sha256':digest}
        if previous and previous.get('state')=='exporting':
            previous=dict(previous,state='waiting',reason='archive report reader interrupted',retry_at=time.time()+30)
            completion_write(stage/'report.json',previous)
        if previous and previous.get('retry_at',0)>time.time():return previous
        if shutil.disk_usage(ROOT).free<20*1024**3:
            return {'state':'waiting','archive_sha256':digest,'reason':'archive report reader requires storage floor'}
        receipt={'state':'exporting','archive_sha256':digest}
        completion_write(stage/'report.json',receipt)
        run(['/usr/bin/systemd-run','--quiet','--collect','--unit='+archive_report_unit(job),
             '--property=RuntimeMaxSec=300','--property=MemoryMax=512M','--property=MemorySwapMax=0',
             '--property=CPUQuota=100%','--property=TasksMax=16','--property=KillMode=control-group',
             '--property=UMask=0077',INSTALL,'_archive-report','--job',job],pass_fds=(lock.fileno(),))
        return receipt


def archive_report_stop(job):
    stage=snapshot_stage(job)
    completion_write(stage/'report-cancel.json',{'epoch':str(uuid.uuid4())})
    with (stage/'report-start.lock').open('a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX)
        if completion_service_active(archive_report_unit(job)):
            run(['/usr/bin/systemctl','stop',archive_report_unit(job)],pass_fds=(lock.fileno(),))
        if completion_service_active(archive_report_unit(job)):raise RuntimeError('archive report reader did not stop')
        if (stage/'report.json').exists():
            previous=archive_report_cached(stage,archive_identity(job)['sha256'])
            if previous.get('state')=='exporting':
                completion_write(stage/'report.json',dict(previous,state='waiting',reason='archive report reader stopped',retry_at=0))
    return {'state':'stopped'}


def archive_report_execute(job):
    current=Path('/proc/self/cgroup').read_text().strip().split('::')[-1]
    if not current.endswith('/'+archive_report_unit(job)):raise RuntimeError('archive report reader outside matching unit')
    stage=snapshot_stage(job);digest=archive_identity(job)['sha256']
    with ARTIFACT_LOCK.open('a') as global_lock,(stage/'export.lock').open('a') as lock:
        fcntl.flock(global_lock,fcntl.LOCK_EX);fcntl.flock(lock,fcntl.LOCK_EX)
        try:
            receipt=archive_report_read(job,digest)
        except (ValueError,UnicodeError,tarfile.TarError) as exc:
            receipt={'state':'unavailable','archive_sha256':digest,'reason':str(exc)[:400]}
        except Exception as exc:
            receipt={'state':'waiting','archive_sha256':digest,'reason':str(exc)[:400],'retry_at':time.time()+60}
        completion_write(stage/'report.json',receipt)
        return 0 if receipt['state']=='ready' else 1


def archive_report_read(job, digest):
    archive=job_path(job)/'artifact.tar.gz'
    if digest_file(archive)!=digest:raise RuntimeError('archived report source checksum mismatch')
    report=None;count=0;total=0
    # Streaming iteration reaches the end to reject a later duplicate. Nothing
    # is extracted and tar link resolution is never used for this fixed member.
    with tarfile.open(archive,'r|gz') as tar:
        for member in tar:
            count+=1;total+=member.size
            if count>100000 or member.size<0 or total>COMPLETION_MAX_BYTES:
                raise ValueError('archived report scan exceeds export bounds')
            if member.name=='work/autonomy-report.json':
                if report is not None:raise ValueError('duplicate archived autonomy report')
                if not member.isfile() or member.islnk() or member.issym() or member.size>128*1024:
                    raise ValueError('archived autonomy report must be an independent regular file at most 128 KiB')
                with tar.extractfile(member) as stream:report=stream.read(128*1024+1)
                if len(report)!=member.size:raise ValueError('truncated archived autonomy report')
            elif Path(member.name).parts==('work','autonomy-report.json'):
                raise ValueError('noncanonical archived autonomy report path')
    if report is None:raise ValueError('immutable export has no root autonomy report')
    text=report.decode('utf-8')  # Historical malformed report text remains evidence.
    if digest_file(archive)!=digest:raise RuntimeError('archived report source changed during scan')
    return {'state':'ready','archive_sha256':digest,'report_sha256':hashlib.sha256(report).hexdigest(),'report':text}


def archive_identity(job):
    """Cheap root receipt identity; consumers still hash bytes before using them."""
    origin = job_path(job)
    state = origin / 'artifact-state'
    info = state.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o022:
        raise RuntimeError('unsafe artifact receipt directory')
    receipt = completion_json(state / 'receipt.json')
    archive = regular(origin / 'artifact.tar.gz')
    digest = receipt.get('sha256', '')
    if receipt.get('state') != 'ready' or len(digest) != 64 or any(c not in '0123456789abcdef' for c in digest):
        raise RuntimeError('trusted artifact identity is unavailable')
    if archive.st_uid != os.geteuid() or archive.st_mode & 0o022 or archive.st_size <= 0:
        raise RuntimeError('unsafe artifact metadata')
    if 'bytes' in receipt and receipt['bytes'] != archive.st_size:
        raise RuntimeError('artifact size differs from trusted receipt')
    return {'state': 'ready', 'sha256': digest}


def completion_copy_unit(job, source, kind):
    job_path(job); job_path(source)
    if kind in ('archive-work','archive-resume'):
        return 'lectern-completion-copy-' + ('work-' if kind=='archive-work' else 'report-resume-') + job + '.service'
    if kind == 'resume':
        return 'lectern-completion-resume-' + job + '.service'
    if kind == 'derived':
        return 'lectern-completion-copy-derived-' + job + '.service'
    if kind == 'derived-review':
        return 'lectern-completion-copy-derived-review-' + job + '-' + source + '.service'
    if kind == 'archive':
        return 'lectern-completion-copy-archive-' + job + '-' + source + '.service'
    raise ValueError('unknown completion copy kind')


def completion_copy_receipt(stage, source, kind):
    if kind in ('archive-work','archive-resume'): return stage / ('copy-work.json' if kind=='archive-work' else 'copy-report-resume.json')
    if kind == 'resume':
        return stage / 'copy-resume.json'
    if kind == 'derived':
        return stage / 'copy-derived.json'
    if kind == 'derived-review':
        job_path(source)
        return stage / ('copy-derived-review-' + source + '.json')
    if kind == 'archive':
        job_path(source)
        return stage / ('copy-archive-' + source + '.json')
    raise ValueError('unknown completion copy kind')


def archive_copy_stage(job):
    destination=job_path(job)
    if not destination.exists():
        destination.mkdir(mode=0o770)
        os.chown(destination,0,pwd.getpwnam('admin').pw_gid);destination.chmod(0o770)
    return completion_stage(job,create=True)


def archive_copy_generation(value):
    if type(value) is not int or not 1<=value<=2**31-1:raise ValueError('invalid archive copy generation')
    return value


def archive_copy_revoked(stage):
    path=stage/'archive-copy-revoked.json'
    return completion_json(path)['generation'] if path.exists() else 0


def archive_copy_status(job,source,kind,generation):
    archive_copy_generation(generation);job_path(source)
    destination=job_path(job)
    if job==source or (destination/'job.json').exists():raise ValueError('archive copy needs unlaunched distinct destination')
    stage=archive_copy_stage(job);path=completion_copy_receipt(stage,source,kind)
    with (stage/'archive-copy.lock').open('a') as guard:
        try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:return {'state':'waiting','copy_source_job':source,'copy_generation':generation,'reason':'archive copy launch or stop in progress'}
        authority=stage/'archive-copy-authority.json'
        old=completion_json(authority) if authority.exists() else {}
        if old and (old['source_job']!=source or old['kind']!=kind):raise ValueError('reserved archive copy input cannot change')
        if generation<=archive_copy_revoked(stage) or generation<old.get('generation',0):
            return {'state':'waiting','copy_source_job':source,'copy_generation':generation,'reason':'archive copy generation revoked','revoked':True}
        completion_write(authority,{'generation':generation,'source_job':source,'kind':kind})
        receipt=completion_json(path) if path.exists() else {}
        if completion_service_active(completion_copy_unit(job,source,kind)):
            return {'state':'copying','copy_source_job':source,'copy_generation':generation}
        if receipt.get('state')=='copied':return dict(receipt,copy_generation=generation)
        if receipt.get('retry_at',0)>time.time():return dict(receipt,copy_generation=generation)
        request={'state':'copying','copy_source_job':source,'copy_generation':generation}
        completion_launch_capacity()
        completion_write(path,request)
        # Revocation is written before stop tries this guard. Recheck before
        # spawning, then retain guard ownership through systemd-run's lifetime.
        if generation<=archive_copy_revoked(stage):return dict(request,state='waiting',revoked=True)
        command='_copy-archive-work' if kind=='archive-work' else '_copy-archive-resume'
        run(['/usr/bin/systemd-run','--quiet','--collect','--unit='+completion_copy_unit(job,source,kind),
             '--property=RuntimeMaxSec=600','--property=MemoryMax=2G','--property=CPUQuota=200%',
             '--property=TasksMax=32','--property=KillMode=control-group','--property=UMask=0077',
             '--property=LimitFSIZE=2147483648',INSTALL,command,'--job',job,'--from-job',source,
             '--copy-generation',str(generation)],pass_fds=(guard.fileno(),))
        return request


def archive_copy_stop(job,generation=None):
    stage=archive_copy_stage(job);authority=stage/'archive-copy-authority.json'
    with (stage/'archive-copy-revoke.lock').open('a') as guard:
        fcntl.flock(guard,fcntl.LOCK_EX)
        known=completion_json(authority).get('generation',0) if authority.exists() else 0
        maximum=max(known,archive_copy_revoked(stage),1 if generation is None else archive_copy_generation(generation))
        completion_write(stage/'archive-copy-revoked.json',{'generation':maximum})
    with (stage/'archive-copy.lock').open('a') as guard:
        try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:return False
        for kind in ('archive-work','archive-resume'):
            name=completion_copy_unit(job,job,kind)
            if completion_service_active(name):run(['/usr/bin/systemctl','stop','--no-block',name],pass_fds=(guard.fileno(),),timeout=3)
            if completion_service_active(name):return False
    return True


def completion_copy_status(job, source, kind, generation=1):
    if kind in ('archive-work','archive-resume'):return archive_copy_status(job,source,kind,generation)
    destination = job_path(job)
    job_path(source)
    if job == source or (destination / 'job.json').exists():
        raise ValueError('completion copy needs a fresh distinct job')
    stage = completion_stage(job, create=True)
    path = completion_copy_receipt(stage, source, kind)
    with path.with_suffix('.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        receipt = completion_json(path) if path.exists() else {}
        if receipt.get('copy_source_job', source) != source:
            raise ValueError('completion copy source cannot change')
        if completion_service_active(completion_copy_unit(job, source, kind)):
            return {'state': 'copying', 'copy_source_job': source}
        if receipt.get('state') == 'copied':
            return receipt
        if receipt.get('state') == 'copying':
            receipt = dict(receipt, state='waiting', reason='copy interrupted; reserved destination retained', retry_at=time.time()+60)
            completion_write(path, receipt)
        if receipt.get('retry_at', 0) > time.time():
            return receipt
        request = {'state': 'copying', 'copy_source_job': source}
        try:
            completion_launch_capacity()
            completion_write(path, request)
            command = {'derived-review':'_copy-derived-review', 'derived': '_copy-derived', 'archive': '_copy-archive-review', 'resume': '_completion-resume'}[kind]
            run(['/usr/bin/systemd-run', '--quiet', '--collect', '--unit='+completion_copy_unit(job, source, kind),
                 '--property=RuntimeMaxSec=600', '--property=MemoryMax=2G', '--property=CPUQuota=200%',
                 '--property=TasksMax=32', '--property=KillMode=control-group', '--property=UMask=0077',
                 INSTALL, command, '--job', job, '--from-job', source])
            return request
        except Exception as exc:
            receipt = dict(request, state='waiting', reason=str(exc)[-500:], retry_at=time.time()+60)
            completion_write(path, receipt)
            return receipt


def completion_archive_volume(job):
    """Recover only our reserved, never-launched destination volume."""
    p=job_path(job);stage=completion_stage(job);marker=stage/'archive-volume.json'
    if (p/'job.json').exists():raise ValueError('cannot prepare volume of launched job')
    image=p/'work.ext4';work=p/'work'
    if not marker.exists():
        if image.exists() or work.exists():return ensure_work(p)
        completion_write(marker,{'state':'preparing','job':job,'capacity_bytes':2*1024**3})
    recorded=completion_json(marker)
    if recorded.get('job')!=job or recorded.get('capacity_bytes')!=2*1024**3:raise ValueError('archive volume reservation differs')
    if recorded.get('state')=='ready':return ensure_work(p)
    work.mkdir(exist_ok=True)
    if work.is_symlink():raise ValueError('linked destination volume')
    if os.path.ismount(work):
        ensure_work(p)
    else:
        if any(work.iterdir()):raise ValueError('unmounted destination contains unreserved files')
        if image.exists():
            info=regular(image)
            if info.st_uid!=os.geteuid() or info.st_size!=2*1024**3:raise ValueError('unsafe partial archive volume')
        else:
            fd=os.open(image,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
            try:os.ftruncate(fd,2*1024**3);os.fsync(fd)
            finally:os.close(fd)
        run(['/usr/sbin/mkfs.ext4','-q','-F',str(image)])
        run(['/usr/bin/mount','-o','loop,nosuid,nodev',str(image),str(work)])
        ensure_work(p)
    lost=work/'lost+found'
    if lost.exists():lost.rmdir()
    admin=pwd.getpwnam('admin');os.chown(work,admin.pw_uid,admin.pw_gid)
    completion_write(marker,dict(recorded,state='ready'))
    return work


def completion_copy_execute(job, source, kind, generation=1):
    stage = completion_stage(job)
    path = completion_copy_receipt(stage, source, kind)
    if kind in ('archive-work','archive-resume'):
        authority=completion_json(stage/'archive-copy-authority.json')
        if authority!={'generation':generation,'source_job':source,'kind':kind} or generation<=archive_copy_revoked(stage):return 1
    with ARTIFACT_LOCK.open('a') as global_lock:
        fcntl.flock(global_lock, fcntl.LOCK_EX)
        try:
            completion_capacity()
            if kind in ('archive-work','archive-resume'):completion_archive_volume(job)
            operation = {'archive-resume':lambda job,source:copy_archive_work(job,source,preserve_report=True),'archive-work':copy_archive_work,'derived-review':lambda job,source:copy_archive_review(job,source,derived=True),'derived': completion_copy_derived, 'archive': copy_archive_review, 'resume': completion_resume}[kind]
            receipt = operation(job, source)
            completion_write(path, dict(receipt, copy_source_job=source))
            return 0
        except Exception as exc:
            completion_write(path, {'state': 'waiting', 'copy_source_job': source,
                'reason': str(exc)[-500:], 'retry_at': time.time()+60})
            return 1


def completion_prepare_commit(stage, work, binding):
    # The frozen baseline is an identity record, not a readiness commit. Finish
    # ownership on every reconciliation before publishing launch readiness.
    admin = pwd.getpwnam('admin')
    for item in (work, *work.rglob('*')):
        os.chown(item, admin.pw_uid, admin.pw_gid)
    receipt = dict(binding, state='ready')
    completion_write(stage / 'prepare-receipt.json', receipt)
    return receipt


def completion_prepare(job, source, reviewer):
    destination = job_path(job)
    job_path(source); job_path(reviewer)
    if len({job, source, reviewer}) != 3 or (destination / 'job.json').exists():
        raise ValueError('completion baseline needs fresh destination and distinct source/reviewer')
    stage = completion_stage(job, create=True)
    with ARTIFACT_LOCK.open('a') as global_lock, (stage / 'prepare.lock').open('a') as lock:
        fcntl.flock(global_lock, fcntl.LOCK_EX)
        fcntl.flock(lock, fcntl.LOCK_EX)
        completion_capacity()
        if (stage / 'baseline.json').exists():
            binding = completion_binding(stage)
            if binding['source_job'] != source or binding.get('review_job') != reviewer:
                raise ValueError('completion source/reviewer binding cannot change')
            if completion_inspect(ensure_work(destination)) != binding['baseline_sha256']:
                raise RuntimeError('prepared work changed before completion launch')
            return completion_prepare_commit(stage, ensure_work(destination), binding)
        work = ensure_work(destination)
        intent_path = stage / 'preparation.json'
        regular(job_path(source) / 'artifact.tar.gz')
        regular(job_path(reviewer) / 'artifact.tar.gz')
        intent = {'source_job': source, 'review_job': reviewer,
                  'source_archive_sha256': digest_file(job_path(source) / 'artifact.tar.gz'),
                  'review_archive_sha256': digest_file(job_path(reviewer) / 'artifact.tar.gz')}
        if intent_path.exists():
            if completion_json(intent_path) != intent:
                raise RuntimeError('completion preparation source identity changed')
            # This exact fresh destination was reserved before our first write;
            # it has never run. Only our reproducible partial preparation is reset.
            for entry in work.iterdir():
                if entry.is_symlink():
                    raise RuntimeError('linked partial preparation path')
                if entry.is_dir(): shutil.rmtree(entry)
                else: regular(entry); entry.unlink()
            if (stage / 'baseline').exists():
                shutil.rmtree(stage / 'baseline')
        else:
            if any(work.iterdir()):
                raise ValueError('completion preparation requires empty work')
            completion_write(intent_path, intent)
        source_hash = completion_extract_archive(source, work)
        if source_hash != intent['source_archive_sha256']:
            raise RuntimeError('source archive changed during preparation')
        prior = work / 'autonomy-report.json'
        if prior.exists() or prior.is_symlink():
            if regular(prior).st_size > 128*1024:
                raise ValueError('inherited completion report exceeds 128 KiB')
            saved = work / '.lectern-reports' / source
            saved.mkdir(parents=True)
            prior.rename(saved / 'autonomy-report.json')
            (saved / 'manifest.json').write_text(json.dumps({'source_job': source,
                'purpose': 'untrusted prior report evidence, not current submission or approval',
                'original_path': 'autonomy-report.json',
                'preserved_path': str((saved / 'autonomy-report.json').relative_to(work)),
                'sha256': digest_file(saved / 'autonomy-report.json')}, indent=2))
        evidence = work / '.lectern-review' / reviewer
        if evidence.exists() or evidence.is_symlink():
            raise ValueError('reviewer evidence destination already exists')
        review_work = evidence / 'work'
        review_hash = completion_extract_archive(reviewer, review_work)
        if review_hash != intent['review_archive_sha256']:
            raise RuntimeError('review archive changed during preparation')
        manifest = []
        for item in completion_paths(review_work):
            manifest.append({'path': str(item.relative_to(review_work)),
                             'kind': 'directory' if item.is_dir() else 'file',
                             'sha256': None if item.is_dir() else digest_file(item), 'link': None})
        (evidence / 'manifest.json').write_text(json.dumps({'source_job': reviewer,
            'source_archive_sha256': review_hash, 'purpose': 'untrusted reviewer evidence, not approval', 'files': manifest}, indent=2))
        baseline = stage / 'baseline'
        completion_copy(work, baseline)
        baseline_hash = completion_inspect(baseline)
        if completion_inspect(work) != baseline_hash:
            raise ValueError('prepared workspace changed while freezing baseline')
        binding = {'policy': 'documentary-v1', 'source_job': source, 'review_job': reviewer,
                   'source_archive_sha256': source_hash, 'review_archive_sha256': review_hash,
                   'baseline_sha256': baseline_hash}
        completion_write(stage / 'baseline.json', binding)
        return completion_prepare_commit(stage, work, binding)


def completion_prepare_unit(job):
    job_path(job)
    return 'lectern-completion-prepare-' + job + '.service'


def completion_prepare_status(job, source, reviewer):
    destination = job_path(job)
    job_path(source); job_path(reviewer)
    if len({job, source, reviewer}) != 3 or (destination / 'job.json').exists():
        raise ValueError('completion preparation needs a fresh distinct job')
    stage = completion_stage(job, create=True)
    with (stage / 'prepare-start.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if completion_service_active(completion_prepare_unit(job)):
            return {'state': 'preparing'}
        path = stage / 'prepare-receipt.json'
        receipt = completion_json(path) if path.exists() else {}
        if receipt.get('state') == 'ready':
            binding = completion_json(stage / 'baseline.json')
            if binding.get('source_job') != source or binding.get('review_job') != reviewer or receipt != dict(binding, state='ready'):
                raise RuntimeError('completion preparation binding mismatch')
            return receipt
        if receipt.get('source_job', source) != source or receipt.get('review_job', reviewer) != reviewer:
            raise ValueError('completion preparation request cannot change')
        if receipt.get('state') == 'preparing':
            receipt = dict(receipt, state='waiting', reason='preparation interrupted; reserved destination retained', retry_at=time.time()+60)
            completion_write(path, receipt)
        if receipt.get('retry_at', 0) > time.time():
            return receipt
        request = {'state': 'preparing', 'source_job': source, 'review_job': reviewer}
        try:
            completion_launch_capacity()
            completion_write(path, request)
            run(['/usr/bin/systemd-run', '--quiet', '--collect', '--unit='+completion_prepare_unit(job),
                 '--property=RuntimeMaxSec=600', '--property=MemoryMax=2G', '--property=CPUQuota=200%',
                 '--property=TasksMax=32', '--property=KillMode=control-group', '--property=UMask=0077',
                 INSTALL, '_completion-prepare', '--job', job, '--from-job', source, '--review-job', reviewer])
            return request
        except Exception as exc:
            receipt = dict(request, state='waiting', reason=str(exc)[-500:], retry_at=time.time()+60)
            completion_write(path, receipt)
            return receipt


def completion_prepare_execute(job, source, reviewer):
    stage = completion_stage(job)
    try:
        completion_prepare(job, source, reviewer)
        return 0
    except Exception as exc:
        completion_write(stage / 'prepare-receipt.json', {'state': 'waiting', 'source_job': source,
            'review_job': reviewer, 'reason': str(exc)[-500:], 'retry_at': time.time()+60})
        return 1


def completion_unit(job):
    job_path(job)
    return 'lectern-completion-' + job + '.service'


def completion_service_active(name):
    result = subprocess.run(['/usr/bin/systemctl', 'show', name, '--property=ActiveState'], capture_output=True, text=True, timeout=3)
    if result.returncode:
        raise RuntimeError('completion validator unit state unavailable')
    values = dict(line.split('=', 1) for line in result.stdout.splitlines() if '=' in line)
    state = values.get('ActiveState')
    if state not in ('inactive', 'failed', 'active', 'activating', 'deactivating', 'reloading'):
        raise RuntimeError('completion validator unit state uncertain')
    return state in ('active', 'activating', 'deactivating', 'reloading')


def completion_active(job):
    return completion_service_active(completion_unit(job))


def completion_stop(job, generation=None):
    """Cancel only this job's bounded documentary helpers, retaining all state."""
    if generation is not None or (job_path(job)/'completion/archive-copy-authority.json').exists():
        if not archive_copy_stop(job,generation):return {'state':'stopping'}
    names = [completion_prepare_unit(job), completion_unit(job)]
    stage = job_path(job) / 'completion'
    if stage.exists():
        stage = completion_stage(job)
        for path in sorted(stage.glob('copy-*.json')):
            receipt = completion_json(path)
            source = receipt.get('copy_source_job')
            if path.name in ('copy-work.json','copy-report-resume.json'):
                continue
            elif path.name == 'copy-resume.json':
                names.append(completion_copy_unit(job, source, 'resume'))
            elif path.name == 'copy-derived.json':
                names.append(completion_copy_unit(job, source, 'derived'))
            elif path.name.startswith('copy-derived-review-'):
                job_path(source)
                if path.name != 'copy-derived-review-' + source + '.json':
                    raise RuntimeError('derived evidence cancellation identity mismatch')
                names.append(completion_copy_unit(job, source, 'derived-review'))
            elif path.name.startswith('copy-archive-'):
                job_path(source)
                if path.name != 'copy-archive-' + source + '.json':
                    raise RuntimeError('completion copy cancellation identity mismatch')
                names.append(completion_copy_unit(job, source, 'archive'))
    for name in names:
        if completion_service_active(name):
            run(['/usr/bin/systemctl', 'stop', name])
        if completion_service_active(name):
            raise RuntimeError('completion helper did not stop: ' + name)
    # Preparing/running receipts intentionally survive. Next enabled poll sees
    # the inactive unit and schedules recovery without consuming another grant.
    return {'state': 'stopped'}


def completion_launch_capacity():
    # Keep controller polls bounded: full retained-tree accounting is done by
    # the worker under ARTIFACT_LOCK before it writes any new copied data.
    if shutil.disk_usage(ROOT).free < 26*1024**3:
        raise RuntimeError('completion helper needs 6 GiB above the 20 GiB free-space floor')


def completion_capacity():
    capacity = storage_status()
    if not capacity['ready'] or capacity['free_bytes'] < 26*1024**3 or capacity['allocated_bytes'] > STORAGE_LIMIT-6*1024**3:
        raise RuntimeError('completion reconstruction needs 6 GiB storage headroom above the 20 GiB floor')


def completion_ready(stage):
    binding = completion_binding(stage)
    receipt = completion_json(stage / 'receipt.json')
    if receipt.get('state') != 'ready':
        raise ValueError('derived completion artifact is not ready')
    archive = stage.parent / 'completion-artifact.tar.gz'
    info = regular(archive)
    if info.st_uid != os.geteuid() or info.st_mode & 0o022:
        raise RuntimeError('unsafe published completion archive')
    if receipt.get('source_archive_sha256') != binding['source_archive_sha256'] or receipt.get('baseline_sha256') != binding['baseline_sha256']:
        raise ValueError('derived completion source binding mismatch')
    if digest_file(archive) != receipt.get('derived_archive_sha256') or completion_inspect(stage / 'derived/work') != receipt.get('derived_tree_sha256'):
        raise ValueError('derived completion artifact checksum mismatch')
    return receipt


def completion_reconstruct(job):
    stage = completion_stage(job)
    if status(job)['state'] != 'done':
        raise ValueError('completion reconstruction requires a completed worker')
    with (stage / 'start.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if completion_active(job):
            return {'state': 'running'}
        receipt = completion_json(stage / 'receipt.json') if (stage / 'receipt.json').exists() else {}
        if receipt.get('state') == 'ready':
            binding = completion_json(stage / 'baseline.json')
            if receipt.get('baseline_sha256') != binding.get('baseline_sha256') or receipt.get('source_archive_sha256') != binding.get('source_archive_sha256'):
                raise RuntimeError('trusted completion receipt binding mismatch')
            return receipt
        if receipt.get('state') == 'rejected':
            return receipt
        if receipt.get('state') == 'running':
            receipt = {'state': 'waiting', 'reason': 'validator interrupted; original workspace retained', 'retry_at': time.time()+60}
            completion_write(stage / 'receipt.json', receipt)
        if receipt.get('retry_at', 0) > time.time():
            return receipt
        completion_launch_capacity()
        completion_write(stage / 'receipt.json', {'state': 'running'})
        try:
            run(['/usr/bin/systemd-run', '--quiet', '--collect', '--unit='+completion_unit(job),
                 '--property=RuntimeMaxSec=600', '--property=MemoryMax=2G', '--property=CPUQuota=200%',
                 '--property=TasksMax=32', '--property=KillMode=control-group', '--property=UMask=0077',
                 INSTALL, '_completion-reconstruct', '--job', job])
        except Exception as exc:
            completion_write(stage / 'receipt.json', {'state': 'waiting', 'reason': str(exc)[-400:], 'retry_at': time.time()+60})
            raise
        return {'state': 'running'}


def completion_transport(stage, work, name='submission.json'):
    report = work / 'autonomy-report.json'
    if report.exists() or report.is_symlink():
        if regular(report).st_size > 128*1024:
            raise CompletionOverlayViolation('completion report exceeds 128 KiB')
        target = stage / name
        if target.exists():
            if target.read_bytes() != report.read_bytes():
                raise RuntimeError('completion submission changed after freezing')
        else:
            with target.open('xb') as stream:
                stream.write(report.read_bytes())
            target.chmod(0o600)


def completion_publish(job, stage, receipt):
    """Publish one readable immutable archive; keep baseline and state private."""
    binding = completion_binding(stage)
    if any(receipt.get(key) != value for key, value in binding.items()):
        raise RuntimeError('completion publication baseline binding changed')
    if completion_inspect(stage / 'derived/work') != receipt.get('derived_tree_sha256'):
        raise RuntimeError('completion publication tree changed')
    target = job_path(job) / 'completion-artifact.tar.gz'
    private = stage / 'derived/artifact.tar.gz'
    if target.exists() or target.is_symlink():
        info = regular(target)
        if info.st_uid != os.geteuid() or info.st_mode & 0o022 or digest_file(target) != receipt.get('derived_archive_sha256'):
            raise RuntimeError('published completion archive conflicts with durable intent')
    else:
        regular(private)
        if digest_file(private) != receipt.get('derived_archive_sha256'):
            raise RuntimeError('completion publication source checksum mismatch')
        os.chown(private, 0, pwd.getpwnam('admin').pw_gid)
        private.chmod(0o440)
        os.replace(private, target)
        fd = os.open(target.parent, os.O_DIRECTORY)
        try: os.fsync(fd)
        finally: os.close(fd)
    completion_write(stage / 'receipt.json', receipt)
    return receipt


def completion_execute(job):
    stage = completion_stage(job)
    with ARTIFACT_LOCK.open('a') as global_lock, (stage / 'execute.lock').open('a') as lock:
        fcntl.flock(global_lock, fcntl.LOCK_EX)
        fcntl.flock(lock, fcntl.LOCK_EX)
        if (stage / 'receipt.json').exists():
            previous = completion_json(stage / 'receipt.json')
            if previous.get('state') == 'ready':
                # Published derived evidence is never reset/rebuilt on a later
                # integrity or inspector failure; retain the original receipt.
                completion_ready(stage)
                return 0
            if previous.get('state') == 'rejected':
                return 2
        try:
            if status(job)['state'] != 'done':
                raise RuntimeError('completion builder is not completed')
            completion_capacity()
            binding = completion_binding(stage)
            publication = stage / 'publication.json'
            if publication.exists():
                completion_publish(job, stage, completion_json(publication))
                return 0
            if (job_path(job) / 'completion-artifact.tar.gz').exists():
                raise RuntimeError('completion archive exists without publication intent')
            candidate, derived = stage / 'candidate', stage / 'derived'
            # Only controller-owned, unpublished scratch is discarded on retry.
            for scratch in (candidate, derived):
                if scratch.is_symlink():
                    raise ValueError('linked completion scratch')
                if scratch.exists():
                    shutil.rmtree(scratch)
            candidate_archive_hash = completion_extract_archive(job, candidate)
            completion_transport(stage, candidate)
            # Only the copied current report is transport, never inherited files.
            current_report = candidate / 'autonomy-report.json'
            if current_report.exists():
                regular(current_report); current_report.unlink()
            derived.mkdir(mode=0o700)
            result = subprocess.run([OVERLAY_CLI, 'autonomy-overlay', str(stage / 'baseline'), str(candidate), str(derived / 'work')], capture_output=True, text=True, timeout=480)
            if result.returncode == 2:
                raise CompletionOverlayViolation('documentary overlay rejected: ' + result.stderr[-400:])
            if result.returncode:
                raise RuntimeError('documentary validator failed operationally: ' + result.stderr[-400:])
            overlay = json.loads(result.stdout)
            if overlay.get('policy') != binding['policy'] or overlay.get('baseline_sha256') != binding['baseline_sha256'] or overlay.get('derived_sha256') != completion_inspect(derived / 'work'):
                raise RuntimeError('documentary validator receipt identity mismatch')
            archive = derived / 'artifact.tar.gz'
            def safe_member(member):
                if not (member.isfile() or member.isdir()):
                    raise ValueError('derived artifact contains a link or special file')
                member.uid = member.gid = 0
                member.uname = member.gname = ''
                return member
            with archive.open('xb') as raw:
                with gzip.GzipFile(fileobj=raw, mode='wb', compresslevel=1, mtime=0) as compressed:
                    with tarfile.open(fileobj=compressed, mode='w|', dereference=False) as out:
                        out.add(derived / 'work', arcname='work', recursive=True, filter=safe_member)
                raw.flush(); os.fsync(raw.fileno())
            archive.chmod(0o400)
            receipt = dict(binding, state='ready', candidate_archive_sha256=candidate_archive_hash, derived_archive_sha256=digest_file(archive),
                           derived_tree_sha256=overlay['derived_sha256'], overlay=overlay)
            completion_write(stage / 'publication.json', receipt)
            completion_publish(job, stage, receipt)
            return 0
        except CompletionOverlayViolation as exc:
            completion_write(stage / 'receipt.json', {'state': 'rejected', 'reason': str(exc)[-500:]})
            return 2
        except Exception as exc:
            completion_write(stage / 'receipt.json', {'state': 'waiting', 'reason': str(exc)[-500:], 'retry_at': time.time()+60})
            return 1


def completion_copy_derived(job, source):
    destination = job_path(job)
    job_path(source)
    if job == source or (destination / 'job.json').exists():
        raise ValueError('derived review requires a fresh prepared destination')
    if completion_active(source):
        raise RuntimeError('derived artifact is still being reconstructed')
    source_stage = completion_stage(source)
    receipt = completion_ready(source_stage)
    destination_stage = completion_stage(job, create=True)
    expected = dict(receipt, builder_job=source)
    with (destination_stage / 'review-copy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        work = ensure_work(destination)
        published = destination_stage / 'review-source.json'
        if published.exists():
            if completion_json(published) != expected or completion_inspect(work) != receipt['derived_tree_sha256']:
                raise RuntimeError('derived reviewer source binding changed')
            return dict(receipt, state='copied')
        intent = destination_stage / 'review-copy.json'
        if intent.exists():
            if completion_json(intent) != expected:
                raise RuntimeError('derived reviewer preparation source changed')
            for entry in work.iterdir():
                if entry.is_symlink():
                    raise RuntimeError('linked partial reviewer preparation')
                if entry.is_dir(): shutil.rmtree(entry)
                else: regular(entry); entry.unlink()
        else:
            if any(work.iterdir()):
                raise ValueError('derived review destination must be empty')
            completion_write(intent, expected)
        completion_copy(source_stage / 'derived/work', work)
        if completion_inspect(work) != receipt['derived_tree_sha256']:
            raise RuntimeError('derived review copy checksum mismatch')
        admin = pwd.getpwnam('admin')
        for item in (work, *work.rglob('*')):
            os.chown(item, admin.pw_uid, admin.pw_gid)
        # Separate from reviewer evidence and never grants builder capability.
        completion_write(published, expected)
        return dict(receipt, state='copied')


def completion_resume(job, source):
    destination, origin = job_path(job), job_path(source)
    if job == source or (destination / 'job.json').exists() or status(source)['state'] == 'running':
        raise ValueError('completion resume needs a stopped builder and fresh destination')
    if completion_active(source):
        raise ValueError('cannot resume a builder with active reconstruction')
    source_stage = completion_stage(source)
    if (source_stage / 'receipt.json').exists():
        raise ValueError('reconstruction has begun; retry validation instead of the model')
    binding = completion_binding(source_stage)
    archive_hash = archive_identity(source)['sha256']
    stage = completion_stage(job, create=True)
    expected = dict(binding, resume_source_job=source, resume_archive_sha256=archive_hash)
    intent = stage / 'resume-intent.json'
    published = stage / 'resume-ready.json'
    with (stage / 'resume.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        work = ensure_work(destination)
        if published.exists():
            receipt = completion_json(published)
            if any(receipt.get(key) != value for key, value in expected.items()):
                raise RuntimeError('published resume source binding changed')
            if completion_binding(stage) != binding or completion_inspect(work) != receipt.get('resumed_tree_sha256'):
                raise RuntimeError('published resume workspace or baseline changed')
            return receipt
        if intent.exists():
            if completion_json(intent) != expected:
                raise RuntimeError('partial resume source binding changed')
            for entry in work.iterdir():
                if entry.is_symlink():
                    raise RuntimeError('linked partial resume workspace')
                if entry.is_dir(): shutil.rmtree(entry)
                else: regular(entry); entry.unlink()
            baseline = stage / 'baseline'
            if baseline.is_symlink():
                raise RuntimeError('linked partial resume baseline')
            if baseline.exists(): shutil.rmtree(baseline)
        else:
            if any(work.iterdir()) or (stage / 'baseline').exists() or (stage / 'baseline.json').exists():
                raise ValueError('completion resume destination must be fresh')
            completion_write(intent, expected)
        # Resume exactly the stopped builder's verified raw export. Mutable
        # leftovers can never drift between operational retry UUIDs.
        actual = completion_extract_archive(source, work, allow_stopped=True)
        if actual != archive_hash:
            raise RuntimeError('resume source archive identity changed')
        completion_transport(stage, work, name='resume-submission.json')
        # This is the same admitted task, including bounded report-schema repair.
        # Keep malformed/current report bytes visible at the documented path;
        # final reconstruction excludes only that root transport file.
        completion_copy(source_stage / 'baseline', stage / 'baseline')
        if completion_inspect(stage / 'baseline') != binding['baseline_sha256']:
            raise RuntimeError('resumed completion baseline checksum mismatch')
        work_hash = completion_inspect(work)
        admin = pwd.getpwnam('admin')
        for item in (work, *work.rglob('*')):
            os.chown(item, admin.pw_uid, admin.pw_gid)
        completion_write(stage / 'baseline.json', binding)
        completion_write(stage / 'prepare-receipt.json', dict(binding, state='ready'))
        receipt = dict(expected, state='copied', resumed_tree_sha256=work_hash)
        completion_write(published, receipt)
        return receipt


def expert_probe(command, job, probe, stream=None, offset=0):
    job_path(job)
    if not probe or len(probe) != 64 or any(c not in '0123456789abcdef' for c in probe):
        raise ValueError('invalid expert probe ID')
    stage = job_path(job) / 'expert-probes' / probe
    helper = EXPERT_PROBE_HELPER
    if stage.exists():
        for directory in (stage.parent, stage):
            st = directory.lstat()
            if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.geteuid() or st.st_mode & 0o077:
                raise ValueError('unsafe expert probe state')
        if (stage / 'helper.py').exists(): helper = stage / 'helper.py'
    if command == '_expert-probe' and helper == EXPERT_PROBE_HELPER:
        raise ValueError('probe helper was not frozen')
    module = python_helper(helper)
    return module.dispatch(globals(), command, job, probe, stream, offset)


def private_integration(args):
    helper = PRIVATE_INTEGRATION_HELPER
    phases = {'integration-prepare':'prepare','integration-audit-copy':'audit','integration-review-copy':'review','integration-seal':'seal','integration-test':'check','integration-test-status':'check','integration-test-output':'check','integration-publish':'publish','private-source-copy':'consume','integration-rollback':'rollback'}
    phase = phases.get(args.command, args.phase)
    if args.command not in ('integration-tip', 'integration-stop'):
        module = python_helper(helper)
        module.R = __import__('types').SimpleNamespace(**globals())
        try:
            _, stage, _, _ = module.request(args.job, args.integration_id, phase, args.check_id)
        except FileNotFoundError:
            if args.command != 'integration-test-status': raise
            stage = None
        if stage is not None and (stage / 'helper.py').exists(): helper = stage / 'helper.py'
        if args.command == '_integration' and (stage is None or helper != stage / 'helper.py'):
            raise ValueError('integration helper was not frozen')
    module = python_helper(helper)
    return module.dispatch(globals(), args.command, args.job, args.integration_id, phase, args.generation, args.check_id, args.stream, args.offset, args.project_id)


def server_observation(command, job=None, observation=None):
    helper=SERVER_OPERATIONS_HELPER
    if command not in ('server-targets','server-observe-stop'):
        job_path(job)
        if not isinstance(observation,str) or len(observation)!=64 or any(c not in '0123456789abcdef' for c in observation):
            raise ValueError('observation ID must be SHA256')
        stage=SERVER_OBSERVATIONS_ROOT/job/'observations'/observation
        for parent in (SERVER_OBSERVATIONS_ROOT, SERVER_OBSERVATIONS_ROOT/job, stage.parent, stage):
            if parent.exists():
                info=parent.lstat()
                if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_mode&0o077:
                    raise ValueError('unsafe server observation state')
        if (stage/'helper.py').exists():helper=stage/'helper.py'
        if command=='_server-observe' and helper!=stage/'helper.py':raise ValueError('observer helper was not frozen')
    module=python_helper(helper)
    return module.dispatch(globals(),command,job,observation)


def server_maintenance_inspect(args):
    operation=args.operation_id;inspection=args.inspection_id
    for value in (operation,inspection):
        if not isinstance(value,str) or len(value)!=64 or any(c not in '0123456789abcdef' for c in value):raise ValueError('inspection identity must be SHA256')
    helper=SERVER_MAINTENANCE_INSPECT_HELPER
    manifest=SERVER_MAINTENANCE_ROOT/'operations'/operation/'inspections'/inspection/'executables.json'
    if manifest.exists():
        info=regular(manifest)
        if info.st_uid!=0 or info.st_mode&0o077:raise ValueError('unsafe inspection executable selection')
        value=json.loads(manifest.read_bytes());digest=value.get('digest')
        if not isinstance(digest,str) or len(digest)!=64 or any(c not in '0123456789abcdef' for c in digest):raise ValueError('invalid inspection executable identity')
        cache=SERVER_MAINTENANCE_TOOLS/digest
        for directory in (SERVER_MAINTENANCE_TOOLS,cache):
            info=directory.lstat()
            if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_mode&0o022:raise ValueError('unsafe inspector cache directory')
        files=value.get('files',{})
        if set(files)!={'autonomy-runner.py','autonomy-server-maintenance.py','autonomy-server-operations.py','autonomy-server-maintenance-inspect.py','entry.py'}:raise ValueError('invalid inspector executable inventory')
        for name,expected in files.items():
            path=cache/name;info=regular(path)
            if info.st_uid!=0 or info.st_mode&0o022 or info.st_size>16*1024*1024 or hashlib.sha256(path.read_bytes()).hexdigest()!=expected:raise ValueError('inspection executable identity changed')
        helper=cache/'autonomy-server-maintenance-inspect.py'
    module=python_helper(helper)
    command=args.command
    if command=='server-maintenance-status':command='server-maintenance-inspect-status'
    return module.dispatch(globals(),command,args.job,operation,inspection,args.generation)


def server_maintenance(args):
    helper = SERVER_MAINTENANCE_HELPER
    operation = getattr(args,'operation_id',None)
    if operation is not None:
        if not isinstance(operation,str) or len(operation)!=64 or any(c not in '0123456789abcdef' for c in operation):
            raise ValueError('maintenance operation must be SHA256')
        manifest = SERVER_MAINTENANCE_ROOT/'operations'/operation/'executables.json'
        if manifest.exists():
            info = regular(manifest)
            if info.st_uid != 0 or info.st_mode & 0o077:
                raise ValueError('unsafe maintenance executable selection')
            selected = json.loads(manifest.read_bytes())
            digest = selected.get('digest')
            if not isinstance(digest,str) or len(digest)!=64 or any(c not in '0123456789abcdef' for c in digest):
                raise ValueError('invalid maintenance executable identity')
            helper = SERVER_MAINTENANCE_TOOLS/digest/'autonomy-server-maintenance.py'
    module = python_helper(helper)
    return module.dispatch(globals(),args.command,args.job,operation,args.phase,args.generation)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['go-runtime','go-runtime-stop','_go-runtime','node-dependencies','node-dependencies-stop','_node-dependencies','copy-derived-review','_copy-derived-review','server-maintenance-inspect','server-maintenance-inspect-status','server-maintenance-inspect-stop','_server-maintenance-inspect','server-maintenance-validate','server-maintenance-backup','server-maintenance-apply','server-maintenance-status','server-maintenance-stop','server-maintenance-reconcile','_server-maintenance','server-targets','server-observe','server-observe-status','server-observe-stop','_server-observe','python-test-runtime', 'integration-tip', 'integration-stop', 'integration-status', 'integration-test-status', 'integration-test-output', '_integration', 'integration-prepare', 'integration-audit-copy', 'integration-review-copy', 'integration-seal', 'integration-test', 'integration-publish', 'private-source-copy', 'integration-rollback', 'expert-probe', 'expert-probe-status', 'expert-probe-stop', 'expert-probe-output', '_expert-probe', 'copy-archive-work', '_copy-archive-work', 'copy-archive-resume', '_copy-archive-resume', 'archive-report','archive-report-stop','_archive-report','python-dependencies','python-dependencies-stop','_python-dependencies','_completion-resume', 'archive-identity', '_copy-derived', '_copy-archive-review', 'copy-archive-review', 'completion-stop', 'completion-prepare', '_completion-prepare', 'completion-reconstruct', '_completion-reconstruct', 'copy-derived', 'completion-resume', 'dependencies', 'dependencies-stop', '_dependencies', 'storage', 'compact', 'probe', 'prepare', 'copy', 'copy-review', 'report', 'archive', 'snapshot', '_snapshot', 'selftest', 'start', 'launch-state', 'status', 'stop', '_execute'])
    parser.add_argument('--dependency-key')
    parser.add_argument('--source-sha256')
    parser.add_argument('--operation-id')
    parser.add_argument('--inspection-id')
    parser.add_argument('--observation-id')
    parser.add_argument('--python-test-key')
    parser.add_argument('--key')
    parser.add_argument('--integration-id')
    parser.add_argument('--generation', type=int, default=1)
    parser.add_argument('--check-id')
    parser.add_argument('--project-id', type=int)
    parser.add_argument('--phase', choices=['prepare','audit','review','seal','check','publish','consume','rollback','validate','backup','apply','reconcile','inspect'])
    parser.add_argument('--job')
    parser.add_argument('--probe-id')
    parser.add_argument('--stream', choices=['stdout', 'stderr'])
    parser.add_argument('--offset', type=int, default=0)
    parser.add_argument('--runtime-seconds', type=int, default=1800)
    parser.add_argument('--copy-generation', type=int)
    parser.add_argument('--from-job')
    parser.add_argument('--review-job')
    parser.add_argument('--provider', choices=['codex', 'claude'])
    parser.add_argument('--model')
    parser.add_argument('--network-selftest', action='store_true', help='selftest through the real scoped public egress proxy')
    parser.add_argument('--hold-seconds', type=int, choices=range(0, 121), default=0, help='selftest only: bounded idle interval for cancellation probes')
    parser.add_argument('--prompt')
    args = parser.parse_args()
    os.umask(0o077)
    if os.geteuid() != 0:
        raise RuntimeError('requires the installed privileged runner')
    if args.command in ('server-maintenance-inspect','server-maintenance-inspect-status','server-maintenance-inspect-stop','_server-maintenance-inspect') or (args.command=='server-maintenance-status' and args.phase=='inspect'):
        out=server_maintenance_inspect(args)
        if args.command=='_server-maintenance-inspect':return out
        print(json.dumps(out,sort_keys=True,separators=(',',':')));return 0
    if args.command.startswith('server-maintenance-') or args.command == '_server-maintenance':
        out = server_maintenance(args)
        if args.command == '_server-maintenance': return out
    elif args.command in ('server-targets','server-observe','server-observe-status','server-observe-stop','_server-observe'):
        out = server_observation(args.command, args.job, args.observation_id)
        if args.command == '_server-observe': return out
    elif args.command in ('go-runtime','go-runtime-stop','_go-runtime'):
        out=go_probe_runtime(args.job,args.dependency_key,args.source_sha256,args.generation,stop=args.command=='go-runtime-stop',execute=args.command=='_go-runtime')
        if args.command=='_go-runtime':return out
    elif args.command == 'python-test-runtime':
        out = python_test_runtime_status(args.key, args.job)
    elif args.command in ('integration-tip', 'integration-stop', 'integration-status', 'integration-test-status', 'integration-test-output', '_integration', 'integration-prepare', 'integration-audit-copy', 'integration-review-copy', 'integration-seal', 'integration-test', 'integration-publish', 'private-source-copy', 'integration-rollback'):
        out = private_integration(args)
        if args.command == '_integration': return out
    elif args.command in ('expert-probe', 'expert-probe-status', 'expert-probe-stop', 'expert-probe-output', '_expert-probe'):
        out = expert_probe(args.command, args.job, args.probe_id, args.stream, args.offset)
        if args.command == '_expert-probe': return out
    elif args.command == 'archive-report':
        out=archive_report(args.job)
    elif args.command == 'archive-report-stop':
        out=archive_report_stop(args.job)
    elif args.command == '_archive-report':
        return archive_report_execute(args.job)
    elif args.command == 'node-dependencies':
        out = node_call('launch',args.job,args.generation)
    elif args.command == 'node-dependencies-stop':
        out = node_call('stop',args.job,args.generation)
    elif args.command == '_node-dependencies':
        return node_call('execute',args.job,args.generation)
    elif args.command == 'python-dependencies':
        out = python_dependencies(args.job)
    elif args.command == 'python-dependencies-stop':
        out = python_dependencies_stop(args.job)
    elif args.command == '_python-dependencies':
        return python_dependencies_execute(args.job)
    elif args.command == 'archive-identity':
        out = archive_identity(args.job)
    elif args.command == 'copy-archive-resume':
        out = completion_copy_status(args.job, args.from_job, 'archive-resume', 1 if args.copy_generation is None else args.copy_generation)
    elif args.command == '_copy-archive-resume':
        return completion_copy_execute(args.job, args.from_job, 'archive-resume', 1 if args.copy_generation is None else args.copy_generation)
    elif args.command == 'copy-archive-work':
        out = completion_copy_status(args.job, args.from_job, 'archive-work', 1 if args.copy_generation is None else args.copy_generation)
    elif args.command == '_copy-archive-work':
        return completion_copy_execute(args.job, args.from_job, 'archive-work', 1 if args.copy_generation is None else args.copy_generation)
    elif args.command == 'copy-derived-review':
        out = completion_copy_status(args.job, args.from_job, 'derived-review')
    elif args.command == '_copy-derived-review':
        return completion_copy_execute(args.job, args.from_job, 'derived-review')
    elif args.command == '_copy-derived':
        return completion_copy_execute(args.job, args.from_job, 'derived')
    elif args.command == '_copy-archive-review':
        return completion_copy_execute(args.job, args.from_job, 'archive')
    elif args.command == 'copy-archive-review':
        out = completion_copy_status(args.job, args.from_job, 'archive')
    elif args.command == 'completion-stop':
        out = completion_stop(args.job, args.copy_generation)
    elif args.command == 'completion-prepare':
        out = completion_prepare_status(args.job, args.from_job, args.review_job)
    elif args.command == '_completion-prepare':
        return completion_prepare_execute(args.job, args.from_job, args.review_job)
    elif args.command == 'completion-reconstruct':
        out = completion_reconstruct(args.job)
    elif args.command == '_completion-reconstruct':
        return completion_execute(args.job)
    elif args.command == 'copy-derived':
        out = completion_copy_status(args.job, args.from_job, 'derived')
    elif args.command == 'completion-resume':
        out = completion_copy_status(args.job, args.from_job, 'resume')
    elif args.command == '_completion-resume':
        return completion_copy_execute(args.job, args.from_job, 'resume')
    elif args.command == 'dependencies':
        out = dependency_status(args.job)
    elif args.command == 'dependencies-stop':
        key = go_dependency_key(ensure_work(job_path(args.job)))
        if key is not None:
            name = 'lectern-dependencies-' + key + '.service'
            active = subprocess.run(['/usr/bin/systemctl', 'is-active', name], capture_output=True, text=True).stdout.strip()
            if active in ('active', 'activating', 'deactivating'):
                run(['/usr/bin/systemctl', 'stop', name])
        out = {'state': 'stopped'}
    elif args.command == '_dependencies':
        return dependency_execute(args.job)
    elif args.command == 'storage':
        out = storage_status()
    elif args.command == 'compact':
        out = compact_assets()
    elif args.command == 'probe':
        # Actual per-job BPF query remains mandatory before the CLI can execute.
        for path in ['/usr/bin/bwrap', '/usr/bin/socat', '/usr/bin/systemd-run',
                     '/usr/sbin/mkfs.ext4', '/usr/sbin/losetup', INSTALL,
                     '/sys/fs/cgroup/cgroup.controllers']:
            if not Path(path).exists():
                raise RuntimeError('missing prerequisite: ' + path)
        probe_name = 'lectern-autonomy-probe-' + str(uuid.uuid4()) + '.service'
        cmd = ['/usr/bin/systemd-run', '--quiet', '--unit=' + probe_name]
        for prop in properties():
            cmd += ['--property=' + prop]
        run(cmd + ['/usr/bin/sleep', '20'])
        try:
            cg = run(['/usr/bin/systemctl', 'show', probe_name, '--property=ControlGroup', '--value']).stdout.strip()
            if not cg.startswith('/system.slice/lectern-autonomy-probe-'):
                raise RuntimeError('unexpected probe cgroup')
            bpf_attached('/sys/fs/cgroup' + cg)
        finally:
            run(['/usr/bin/systemctl', 'stop', probe_name])
        out = {'ready': True}
    elif args.command == 'snapshot':
        out = snapshot(args.job)
    elif args.command == '_snapshot':
        return snapshot_execute(args.job)
    elif args.command == 'archive':
        archive(args.job)
        return 0
    elif args.command == 'report':
        report(args.job)
        return 0
    elif args.command == 'copy-review':
        out = copy_job(args.job, args.from_job, review=True)
    elif args.command == 'copy':
        out = copy_job(args.job, args.from_job)
    elif args.command == 'prepare':
        out = prepare(args.job)
    elif args.command == '_execute':
        return execute(args.job)
    elif args.command in ('start', 'selftest'):
        if args.command == 'selftest':
            args.provider = 'selftest'
        if not args.provider or not args.prompt:
            raise ValueError('start requires provider and prompt')
        out = start(args)
    elif args.command == 'launch-state':
        out = launch_state(args.job)
    elif args.command == 'stop':
        out = stop(args.job)
    else:
        out = status(args.job)
    print(json.dumps(out))
    return 0

if __name__ == '__main__':
    try:
        sys.exit(main())
    except Exception as exc:
        print(json.dumps({'error': str(exc)}), file=sys.stderr)
        sys.exit(1)
