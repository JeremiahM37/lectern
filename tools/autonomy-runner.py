#!/usr/bin/python3
"""Privileged launcher. Install root-owned; never expose _execute via sudoers."""
import argparse
import ctypes
import ipaddress
import json
import os
from pathlib import Path
import platform
import pwd
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
UID = GID = 65534
DENY = ['0.0.0.0/8', '10.0.0.0/8', '100.64.0.0/10',
        '169.254.0.0/16', '172.16.0.0/12', '192.168.0.0/16', '224.0.0.0/4',
        '240.0.0.0/4', '::/128', '::ffff:0:0/96', 'fc00::/7',
        'fe80::/10', 'ff00::/8']
AUTH = {'codex': Path('/home/admin/.codex/auth.json'),
        'claude': Path('/home/admin/.claude/.credentials.json')}
BIN = {'codex': Path('/home/admin/.local/bin/codex'),
       'claude': Path('/home/admin/.local/bin/claude')}

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
    cmd += ['--proc', '/proc', '--dev', '/dev', '--tmpfs', '/tmp', '--tmpfs', '/run',
            '--tmpfs', '/home', '--dir', '/home/agent', '--dir', '/etc',
            '--ro-bind', '/etc/ssl/certs', '/etc/ssl/certs',
            '--ro-bind', str(assets / 'resolv.conf'), '/etc/resolv.conf',
            '--ro-bind', str(assets / 'passwd'), '/etc/passwd',
            '--ro-bind', str(assets / 'group'), '/etc/group',
            '--ro-bind', str(assets / 'nsswitch.conf'), '/etc/nsswitch.conf',
                        '--ro-bind', str(assets / 'prompt.txt'), '/prompt.txt',
            '--bind', str(work), '/work', '--chdir', '/work']
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


def start(args):
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
        previous = status(args.job)
        if previous['state'] == 'running':
            return previous
        raise ValueError('job UUID has already been used; preserve it and prepare a fresh UUID')
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
        shutil.copyfile(binary, assets / 'agent')
        if args.provider == 'codex':
            helper = binary.parent / 'codex-code-mode-host'
            regular(helper)
            shutil.copyfile(helper, assets / 'codex-code-mode-host')
            (assets / 'codex-code-mode-host').chmod(0o555)
        (assets / 'agent').chmod(0o555)
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
    run(cmd + [INSTALL, '_execute', '--job', args.job], env={'PATH': '/usr/sbin:/usr/bin:/sbin:/bin'})
    return {'state': 'running', 'exit_code': None}

def status(job):
    p = job_path(job)
    if (p / 'result.json').exists():
        return json.loads((p / 'result.json').read_text())
    if (p / 'stopped').exists():
        return {'state': 'stopped', 'exit_code': None}
    r = subprocess.run(['/usr/bin/systemctl', 'show', unit(job), '--property=ActiveState,ExecMainStatus,Result'],
                       text=True, capture_output=True)
    props = dict(line.split('=', 1) for line in r.stdout.splitlines() if '=' in line)
    if props.get('ActiveState') in ('active', 'activating', 'deactivating'):
        return {'state': 'running', 'exit_code': None}
    return {'state': 'failed', 'exit_code': int(props.get('ExecMainStatus', '1')) or 1}

def allocated_storage():
    # The image already accounts for mounted work. Include binaries, logs and
    # all other job data without double-counting that filesystem or hardlinks.
    total = 0
    seen = set()
    for current, dirs, files in os.walk(ROOT, followlinks=False):
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
    if allocated >= 50 * 1024**3:
        raise RuntimeError('retained job storage exceeds 50 GiB; archive before continuing')
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


def copy_job(job, source):
    destination = job_path(job)
    origin = job_path(source)
    if job == source or (destination / 'job.json').exists():
        raise ValueError('copy destination must be a fresh prepared job')
    if status(source)['state'] == 'running':
        raise ValueError('cannot copy a running job')
    dest = ensure_work(destination)
    src = ensure_work(origin)
    if any(dest.iterdir()):
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
    admin = pwd.getpwnam('admin')
    for item in paths:
        rel = item.relative_to(src)
        if rel == Path('autonomy-report.json'):
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['probe', 'prepare', 'copy', 'report', 'archive', 'selftest', 'start', 'status', 'stop', '_execute'])
    parser.add_argument('--job')
    parser.add_argument('--from-job')
    parser.add_argument('--provider', choices=['codex', 'claude'])
    parser.add_argument('--model')
    parser.add_argument('--network-selftest', action='store_true', help='selftest through the real scoped public egress proxy')
    parser.add_argument('--hold-seconds', type=int, choices=range(0, 121), default=0, help='selftest only: bounded idle interval for cancellation probes')
    parser.add_argument('--prompt')
    args = parser.parse_args()
    os.umask(0o077)
    if os.geteuid() != 0:
        raise RuntimeError('requires the installed privileged runner')
    if args.command == 'probe':
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
    elif args.command == 'archive':
        archive(args.job)
        return 0
    elif args.command == 'report':
        report(args.job)
        return 0
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
    elif args.command == 'stop':
        p = job_path(args.job)
        run(['/usr/bin/systemctl', 'stop', unit(args.job)])
        if not (p / 'stopped').exists():
            write_new(p / 'stopped', '')
        out = status(args.job)
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
