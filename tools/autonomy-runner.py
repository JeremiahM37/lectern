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
from pathlib import Path
import platform
import pwd
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
            'TasksMax=256', 'NoNewPrivileges=yes', 'RestrictSUIDSGID=yes',
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

def bwrap(p, provider, assets, work, bridges):
    assets = Path(assets)
    cmd = ['/usr/bin/bwrap', '--die-with-parent', '--new-session', '--unshare-user',
           '--unshare-pid', '--unshare-ipc', '--unshare-uts', '--unshare-net', '--cap-drop', 'ALL',
           '--clearenv', '--setenv', 'HOME', '/home/agent', '--setenv', 'PATH', '/usr/bin:/bin',
           '--setenv', 'LANG', 'C.UTF-8', '--setenv', 'TERM', 'xterm-256color',
           '--setenv', 'TMPDIR', '/tmp', '--tmpfs', '/', '--ro-bind', '/usr', '/usr']
    for path in ('/lib', '/lib64', '/bin', '/sbin'):
        if Path(path).exists():
            cmd += ['--ro-bind', path, path]
    bundle = go_dependency_bundle(Path(work))
    if bundle is not None:
        cmd += ['--ro-bind', str(bundle / 'mod'), '/opt/go-modules',
                '--ro-bind', str(bundle / 'manifest.json'), '/opt/go-dependencies.json',
                '--setenv', 'GOMODCACHE', '/opt/go-modules',
                '--setenv', 'GOPATH', '/tmp/go', '--setenv', 'GOCACHE', '/tmp/go-build',
                '--setenv', 'GOPROXY', 'off', '--setenv', 'GOSUMDB', 'off',
                '--setenv', 'GOTOOLCHAIN', 'local', '--setenv', 'GOENV', 'off',
                '--setenv', 'GOWORK', 'off',
                '--setenv', 'PATH', '/usr/local/go/bin:/usr/bin:/bin']
    cmd += python_test_runtime_mount()
    cmd += ['--proc', '/proc', '--dev', '/dev', '--tmpfs', '/tmp', '--tmpfs', '/run',
            '--tmpfs', '/home', '--dir', '/home/agent', '--dir', '/etc',
            '--ro-bind', '/etc/ssl/certs', '/etc/ssl/certs',
            '--ro-bind', str(assets / 'resolv.conf'), '/etc/resolv.conf',
            '--ro-bind', str(assets / 'passwd'), '/etc/passwd',
            '--ro-bind', str(assets / 'group'), '/etc/group',
            '--ro-bind', str(assets / 'nsswitch.conf'), '/etc/nsswitch.conf',
                        '--ro-bind', str(assets / 'prompt.txt'), '/prompt.txt',
            '--bind', str(work), '/work', '--chdir', '/work']
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
    launch = '/usr/bin/socat TCP4-LISTEN:18080,bind=127.0.0.1,reuseaddr,fork UNIX-CONNECT:/network.sock &\nexec ' + shlex.join(cli)
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
    provider = json.loads(meta.read_text())['provider']
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
    command = bwrap(p, provider, '/tmp/assets', '/tmp/work', '/tmp/bridges')
    def drop():
        os.setgroups([])
        os.setgid(GID)
        os.setuid(UID)
    with (p / 'assets/prompt.txt').open('rb') as prompt:
        process = subprocess.Popen(command, stdin=prompt, preexec_fn=drop, close_fds=True)
        while process.poll() is None:
            try:
                heartbeat = regular(p / 'heartbeat')
                fresh = 0 <= time.time() - heartbeat.st_mtime < 60
            except OSError:
                fresh = False
            if not fresh:
                write_new(p / 'result.json', json.dumps({'state': 'failed', 'exit_code': 124,
                                                       'reason': 'controller heartbeat expired'}))
                # Exact job cgroup only, including this trusted supervisor. No
                # detached grandchildren may outlive the usage monitor.
                run(['/usr/bin/systemctl', 'kill', '--kill-whom=all', '--signal=SIGKILL', unit(job)])
                raise RuntimeError('job cgroup termination unexpectedly returned')
            time.sleep(2)
        result = process
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
    write_new(p / 'job.json', json.dumps({'provider': args.provider, 'model': args.model,
                                               'selftest_hold': args.hold_seconds if args.provider == 'selftest' else 0,
                                               'network_selftest': args.network_selftest if args.provider == 'selftest' else False}))
    write_new(p / 'heartbeat', '', 0o600)
    os.chown(p / 'heartbeat', pwd.getpwnam('admin').pw_uid, pwd.getpwnam('admin').pw_gid)
    for name in ('output.jsonl', 'stderr.log'):
        write_new(p / name, '')
    cmd = ['/usr/bin/systemd-run', '--quiet', '--unit=' + unit(args.job)]
    for prop in properties() + ['StandardOutput=append:' + str(p / 'output.jsonl'),
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
        elif active not in ('inactive', 'failed'):
            raise RuntimeError('runner unit status unavailable during stop')
        return status_unlocked(job)


def status_unlocked(job):
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


def python_test_runtime_mount():
    # Optional tooling must not prevent unrelated workers from launching. An
    # invalid bundle is never mounted; its unavailability is explicit in-worker.
    try:
        bundle = python_test_bundle()
    except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
        reason = str(error) if isinstance(error, ValueError) else type(error).__name__
        return ['--setenv', 'LECTERN_PYTHON_TEST_RUNTIME_STATUS', 'unavailable',
                '--setenv', 'LECTERN_PYTHON_TEST_RUNTIME_REASON', reason[:200]]
    if bundle is None:
        return ['--setenv', 'LECTERN_PYTHON_TEST_RUNTIME_STATUS', 'not_provisioned']
    return ['--ro-bind', str(bundle / 'site-packages'), '/opt/python-test',
            '--ro-bind', str(bundle / 'manifest.json'), '/opt/python-test-runtime.json',
            '--setenv', 'PYTHONPATH', '/opt/python-test',
            '--setenv', 'PYTHONDONTWRITEBYTECODE', '1',
            '--setenv', 'LECTERN_PYTHON_TEST_RUNTIME_STATUS', 'verified']


def python_test_bundle():
    root = DEPENDENCIES / 'python'
    active = root / 'active.json'
    if not active.exists() and not active.is_symlink():
        return None
    def trusted(path, directory=False):
        st = path.lstat()
        expected = stat.S_ISDIR(st.st_mode) if directory else stat.S_ISREG(st.st_mode)
        if not expected or st.st_uid != 0 or st.st_mode & (0o022 if directory else 0o222):
            raise ValueError('unsafe Python runtime path')
        return st
    for path in (DEPENDENCIES, root): trusted(path, True)
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


def go_dependency_bundle(work):
    key = go_dependency_key(work)
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
    for current, dirs, files in (row for root in (ROOT, ASSET_CACHE, DEPENDENCIES) for row in os.walk(root, followlinks=False)):
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


def copy_archive_review(job, source):
    """Give a plan auditor exact exported evidence rather than mutable work."""
    destination = job_path(job)
    job_path(source)
    if job == source or (destination / 'job.json').exists():
        raise ValueError('archive review evidence requires a fresh prepared job')
    stage = completion_stage(job, create=True)
    identity = archive_identity(source)['sha256']
    expected = {'source_job': source, 'source_archive_sha256': identity}
    intent = stage / ('archive-review-' + source + '-intent.json')
    published = stage / ('archive-review-' + source + '-ready.json')
    with intent.with_suffix('.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        work = ensure_work(destination)
        evidence_root = work / '.lectern-review'
        if evidence_root.is_symlink() or (evidence_root.exists() and not evidence_root.is_dir()):
            raise ValueError('unsafe archive review evidence root')
        evidence = evidence_root / source
        if published.exists():
            receipt = completion_json(published)
            if receipt.get('source_archive_sha256') != identity or receipt.get('source_job') != source:
                raise RuntimeError('archive review source identity changed')
            if completion_inspect(evidence / 'work') != receipt.get('evidence_tree_sha256'):
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
        digest = completion_extract_archive(source, copied)
        if digest != identity:
            raise RuntimeError('archive changed during review copy')
        manifest = []
        for item in completion_paths(copied):
            manifest.append({'path': str(item.relative_to(copied)),
                'kind': 'directory' if item.is_dir() else 'file',
                'sha256': None if item.is_dir() else digest_file(item), 'link': None})
        (evidence / 'manifest.json').write_text(json.dumps({'source_job': source,
            'source_archive_sha256': digest, 'purpose': 'untrusted archived evidence, not approval', 'files': manifest}, indent=2))
        tree_hash = completion_inspect(copied)
        admin = pwd.getpwnam('admin')
        for item in (evidence_root, evidence, *evidence.rglob('*')):
            os.chown(item, admin.pw_uid, admin.pw_gid)
        receipt = dict(expected, state='copied', evidence_tree_sha256=tree_hash)
        completion_write(published, receipt)
        return receipt


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
    if kind == 'resume':
        return 'lectern-completion-resume-' + job + '.service'
    if kind == 'derived':
        return 'lectern-completion-copy-derived-' + job + '.service'
    if kind == 'archive':
        return 'lectern-completion-copy-archive-' + job + '-' + source + '.service'
    raise ValueError('unknown completion copy kind')


def completion_copy_receipt(stage, source, kind):
    if kind == 'resume':
        return stage / 'copy-resume.json'
    if kind == 'derived':
        return stage / 'copy-derived.json'
    if kind == 'archive':
        job_path(source)
        return stage / ('copy-archive-' + source + '.json')
    raise ValueError('unknown completion copy kind')


def completion_copy_status(job, source, kind):
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
            command = {'derived': '_copy-derived', 'archive': '_copy-archive-review', 'resume': '_completion-resume'}[kind]
            run(['/usr/bin/systemd-run', '--quiet', '--collect', '--unit='+completion_copy_unit(job, source, kind),
                 '--property=RuntimeMaxSec=600', '--property=MemoryMax=2G', '--property=CPUQuota=200%',
                 '--property=TasksMax=32', '--property=KillMode=control-group', '--property=UMask=0077',
                 INSTALL, command, '--job', job, '--from-job', source])
            return request
        except Exception as exc:
            receipt = dict(request, state='waiting', reason=str(exc)[-500:], retry_at=time.time()+60)
            completion_write(path, receipt)
            return receipt


def completion_copy_execute(job, source, kind):
    stage = completion_stage(job)
    path = completion_copy_receipt(stage, source, kind)
    with ARTIFACT_LOCK.open('a') as global_lock:
        fcntl.flock(global_lock, fcntl.LOCK_EX)
        try:
            completion_capacity()
            operation = {'derived': completion_copy_derived, 'archive': copy_archive_review, 'resume': completion_resume}[kind]
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
    result = subprocess.run(['/usr/bin/systemctl', 'show', name, '--property=ActiveState'], capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError('completion validator unit state unavailable')
    values = dict(line.split('=', 1) for line in result.stdout.splitlines() if '=' in line)
    state = values.get('ActiveState')
    if state not in ('inactive', 'failed', 'active', 'activating', 'deactivating', 'reloading'):
        raise RuntimeError('completion validator unit state uncertain')
    return state in ('active', 'activating', 'deactivating', 'reloading')


def completion_active(job):
    return completion_service_active(completion_unit(job))


def completion_stop(job):
    """Cancel only this job's bounded documentary helpers, retaining all state."""
    names = [completion_prepare_unit(job), completion_unit(job)]
    stage = job_path(job) / 'completion'
    if stage.exists():
        stage = completion_stage(job)
        for path in sorted(stage.glob('copy-*.json')):
            receipt = completion_json(path)
            source = receipt.get('copy_source_job')
            if path.name == 'copy-resume.json':
                names.append(completion_copy_unit(job, source, 'resume'))
            elif path.name == 'copy-derived.json':
                names.append(completion_copy_unit(job, source, 'derived'))
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['_completion-resume', 'archive-identity', '_copy-derived', '_copy-archive-review', 'copy-archive-review', 'completion-stop', 'completion-prepare', '_completion-prepare', 'completion-reconstruct', '_completion-reconstruct', 'copy-derived', 'completion-resume', 'dependencies', 'dependencies-stop', '_dependencies', 'storage', 'compact', 'probe', 'prepare', 'copy', 'copy-review', 'report', 'archive', 'snapshot', '_snapshot', 'selftest', 'start', 'launch-state', 'status', 'stop', '_execute'])
    parser.add_argument('--job')
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
    if args.command == 'archive-identity':
        out = archive_identity(args.job)
    elif args.command == '_copy-derived':
        return completion_copy_execute(args.job, args.from_job, 'derived')
    elif args.command == '_copy-archive-review':
        return completion_copy_execute(args.job, args.from_job, 'archive')
    elif args.command == 'copy-archive-review':
        out = completion_copy_status(args.job, args.from_job, 'archive')
    elif args.command == 'completion-stop':
        out = completion_stop(args.job)
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
