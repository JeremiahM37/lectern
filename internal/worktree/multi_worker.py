"""Owned grouped worktrees with durable progress and conservative cleanup."""
import fcntl
import json
import os
import pathlib
import signal
import subprocess
import sys
import stat
import tempfile
import time

action, raw, validation, single = sys.argv[1:5]
operation_timeout = float(sys.argv[5]) if len(sys.argv) > 5 else 105
operation_deadline = time.monotonic() + operation_timeout
p = json.loads(raw)
root = pathlib.Path(p['path'])
lock = None
owned = False
control = None
namespace = {'__name__': 'workspace_validation'}
exec(compile(validation, '<workspace-validation>', 'exec'), namespace)


def regular_file(name, flags):
    fd = os.open(root / name, flags | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    if not stat.S_ISREG(os.fstat(fd).st_mode):
        os.close(fd)
        raise ValueError('Workspace metadata must be a regular file: ' + name)
    return os.fdopen(fd, 'r+' if flags & os.O_RDWR else 'r')


def acquire_lock():
    # The status endpoint briefly takes the same exclusive lock to obtain a
    # coherent receipt snapshot. Give that probe a bounded grace period; a
    # real mutating worker still holds the lock past the deadline and is
    # rejected rather than being overlapped.
    deadline = min(operation_deadline, time.monotonic() + .25)
    while True:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return
        except BlockingIOError:
            if time.monotonic() >= deadline:
                raise ValueError('A workspace operation is still running; try again after it finishes')
            time.sleep(.01)


def record_name(kind):
    # A workspace made before the rename keeps its files under the old prefix;
    # they hold its lock identity, so they cannot be renamed underneath a
    # running operation. A workspace with neither gets the current name.
    current, legacy = '.lectern-' + kind, '.agentdeck-' + kind
    if (root / current).exists() or not (root / legacy).exists():
        return current
    return legacy


def read_record(name):
    with regular_file(name, os.O_RDONLY) as file:
        return json.load(file)


def write_record(name, value):
    # Never truncate an existing path: it may have been replaced by a symlink
    # or hard link. A new private inode is atomically installed under the lock.
    fd, temp = tempfile.mkstemp(prefix='.lectern-write-', dir=root)
    try:
        with os.fdopen(fd, 'w') as file:
            json.dump(value, file)
            file.flush()
            os.fsync(file.fileno())
        os.replace(temp, root / name)
        directory = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.lexists(temp):
            os.unlink(temp)


def save():
    write_record(record_name('state.json'), p)


def identity(plan):
    return (plan['path'], plan['token'], plan['branch'],
            [(r['worktree']['repo'], r['worktree']['path'], r['worktree']['token'],
              r['worktree']['branch']) for r in plan['repositories']])


def prefix_identity(older, newer):
    left,right=identity(older),identity(newer)
    return left[:3]==right[:3] and bool(left[3]) and left[3]==right[3][:len(left[3])]


def process_starttime(pid):
    # Linux exposes a monotonically increasing start tick in /proc/<pid>/stat.
    # A PID (and therefore a PGID, whose leader is the child PID below) can be
    # reused after the recorded worker exits, so the number alone is not an
    # identity.  Targets without procfs rely on the workspace flock instead.
    try:
        fields = (pathlib.Path('/proc') / str(pid) / 'stat').read_text().rsplit(')', 1)[1].split()
        return fields[19]
    except (FileNotFoundError, ProcessLookupError, IndexError, OSError):
        return None


def boot_id():
    try:
        return pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()
    except (FileNotFoundError, OSError):
        return None


def busy():
    receipt = root / record_name('process.json')
    if os.path.lexists(receipt):
        record = read_record(record_name('process.json'))
        pgid = record['pgid']
        if type(pgid) is not int or pgid <= 1:
            raise ValueError('Workspace process receipt is invalid; inspect it before cleanup')
        expected_start = record.get('starttime')
        expected_boot = record.get('boot_id')
        current_boot = boot_id()
        # A boot-id mismatch proves this record cannot describe a live worker.
        # A start-time mismatch is equally conclusive while the recorded
        # leader still exists. Missing metadata or a missing leader stays on
        # the conservative PGID path below: an orphaned descendant may still
        # be writing even after its leader exits.
        if expected_boot and current_boot and expected_boot != current_boot:
            return
        if isinstance(expected_start, str) and expected_start:
            actual_start = process_starttime(pgid)
            if actual_start is not None and actual_start != expected_start:
                return
        try:
            os.killpg(pgid, 0)
        except ProcessLookupError:
            return
        # An orphaned worker can remain a zombie under a slow PID 1. Zombies
        # cannot write into the workspace and must not make recovery impossible.
        if pathlib.Path('/proc/self/stat').exists():
            active = False
            for process in pathlib.Path('/proc').iterdir():
                if not process.name.isdigit():
                    continue
                try:
                    fields = (process / 'stat').read_text().rsplit(')', 1)[1].split()
                except (FileNotFoundError, ProcessLookupError):
                    continue
                if int(fields[2]) == pgid and fields[0] not in ('Z', 'X'):
                    active = True
                    break
            if not active:
                return
        raise ValueError('A workspace operation is still running; try again after it finishes')


def check_terminals():
    panes = subprocess.run(['tmux', 'list-panes', '-a', '-F', '#{pane_current_path}'],
                           capture_output=True, text=True, timeout=10)
    if panes.returncode and not any(text in panes.stderr.lower() for text in
                                   ('no server running', 'no such file or directory')):
        raise ValueError('Could not check active terminals; nothing was removed')
    for cwd in panes.stdout.splitlines():
        if cwd and os.path.commonpath([os.path.realpath(cwd), str(root)]) == str(root):
            raise ValueError('A terminal is still using this workspace; end or leave it first')


def run_child(entry, operation):
    budget = operation_deadline - time.monotonic()
    if budget <= 0: raise ValueError('Workspace operation timed out; allocation retained for inspection')
    if control: control.check()
    busy()
    child_plan = dict(entry['worktree'])
    if operation == 'create':
        child_plan['base'] = child_plan['commit']
    read_fd, write_fd = os.pipe()
    # Child must not mutate before its process group is durably recorded. EOF
    # means the supervisor died before permission, so exit without running Git.
    wrapper = "import os,sys; gate=int(sys.argv.pop(1)); allowed=os.read(gate,1); os.close(gate); exec(compile(sys.argv.pop(1),'<owned-worktree>','exec')) if allowed==b'1' else sys.exit(1)"
    child = None
    try:
        child = subprocess.Popen(
            ['python3', '-c', wrapper, str(read_fd), single, operation,
             json.dumps(child_plan), str(lock.fileno()), str(max(.01, budget-1)), json.dumps(p)],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
            start_new_session=True, pass_fds=(read_fd, lock.fileno()),
        )
        write_record(record_name('process.json'), {
            'pgid': child.pid,
            'starttime': process_starttime(child.pid),
            'boot_id': boot_id(),
        })
        os.write(write_fd, b'1')
    finally:
        os.close(read_fd)
        os.close(write_fd)
    try:
        stdout, stderr = control.wait(child, budget) if control else child.communicate(timeout=budget)
    except subprocess.TimeoutExpired:
        if child.poll() is None: os.killpg(child.pid, signal.SIGKILL)
        child.communicate()
        raise ValueError('Repository operation timed out; allocation retained for inspection')
    try:
        result = json.loads(stdout)
    except ValueError:
        raise ValueError('Repository worker did not return a result: ' + stderr.strip())
    if operation == 'check-recover':
        if child.returncode:raise ValueError(result.get('error') or 'Repository validation failed')
        return
    if result.get('workspace'):
        result['workspace']['base'] = entry['worktree']['base']
        entry['worktree'] = result['workspace']
    save()
    if child.returncode:
        raise ValueError(result.get('error') or 'Repository operation failed')


try:
    if action not in ('create', 'extend', 'remove', 'check-remove', 'status', 'recover'):
        raise ValueError('Unknown workspace operation')
    if action == 'status':
        # The writer atomically replaces its receipt, so readers can inspect
        # progress while a checkout holds the operation lock. Never save here.
        if root.is_symlink() or not root.is_dir():
            raise ValueError('Workspace root is missing or replaced')
        lock = regular_file(record_name('lock'), os.O_RDONLY)
        saved = read_record(record_name('state.json'))
        if not (prefix_identity(saved,p) or prefix_identity(p,saved)):
            raise ValueError('Workspace ownership or repository allocation does not match')
        lock_stat = os.fstat(lock.fileno())
        if saved.get('operation_lock') != [lock_stat.st_dev, lock_stat.st_ino]:
            raise ValueError('Workspace operation lock was replaced')
        active=False
        try:fcntl.flock(lock.fileno(),fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:active=True
        else:fcntl.flock(lock.fileno(),fcntl.LOCK_UN)
        if not active:
            try:busy()
            except ValueError:active=True
        saved['operation_active']=active
        print(json.dumps({'workspace': saved}))
        sys.exit(0)
    if action == 'create':
        control = SetupControl(p)
        control.check()
        p = namespace['preflight'](p)
        root = pathlib.Path(p['path'])
        root.parent.mkdir(parents=True, exist_ok=True)
        root.mkdir(mode=0o700)
        lock = regular_file('.lectern-lock', os.O_RDWR | os.O_CREAT | os.O_EXCL)
        acquire_lock()
        lock_stat = os.fstat(lock.fileno())
        p['operation_lock'] = [lock_stat.st_dev, lock_stat.st_ino]
        owned = True
        save()
        for entry in p['repositories']:
            # Resolve all refs before the first mutation; use those exact commits
            # so a moving branch cannot silently change a later repository base.
            run_child(entry, 'create')
        busy()
        p['state'] = 'ready'
        p.pop('error', None)
        save()
    else:
        if root.is_symlink() or not root.is_dir():
            raise ValueError('Workspace root is missing or replaced; inspect it before cleanup')
        lock = regular_file(record_name('lock'), os.O_RDWR)
        acquire_lock()
        saved = read_record(record_name('state.json'))
        if action == 'extend':
            if not prefix_identity(saved,p) or len(p['repositories']) != len(saved['repositories'])+1:
                raise ValueError('Extension must preserve every existing repository and add exactly one')
            if any(r['worktree']['state']!='ready' for r in saved['repositories']):
                raise ValueError('Recover incomplete repositories before extending the workspace')
            if not p.get('control_token') or p['control_token'] in (saved.get('control_token'),saved['token']):
                raise ValueError('Extension requires a fresh cancellation identity')
            lock_stat=os.fstat(lock.fileno())
            if saved.get('operation_lock') != [lock_stat.st_dev,lock_stat.st_ino]:
                raise ValueError('Workspace operation lock was replaced')
            busy()
            candidate=dict(saved)
            candidate.update(repositories=saved['repositories']+[p['repositories'][-1]],control_token=p['control_token'],state='extending')
            p=namespace['preflight'](candidate,len(saved['repositories']))
            control=SetupControl(p);control.check()
            owned=True
            save()  # Publish the full allocation before the new child can mutate.
            run_child(p['repositories'][-1],'create')
            busy();p['state']='ready';p.pop('error',None);save()
            print(json.dumps({'workspace':p}));sys.exit(0)
        if identity(saved) != identity(p):
            raise ValueError('Workspace ownership or repository allocation does not match')
        lock_stat = os.fstat(lock.fileno())
        if saved.get('operation_lock') != [lock_stat.st_dev, lock_stat.st_ino]:
            raise ValueError('Workspace operation lock was replaced; inspect it before cleanup')
        p = saved
        owned = True
        busy()
        check_terminals()
        if action == 'recover':
            for entry in p['repositories']:run_child(entry,'check-recover')
            for entry in p['repositories']:run_child(entry,'recover')
            p.update(state='failed',error='Interrupted checkout validated; files retained for inspection')
            save()
            print(json.dumps({'workspace':p}))
            sys.exit(0)
        allowed = {'.lectern-lock', '.lectern-state.json', '.lectern-process.json',
                   '.agentdeck-lock', '.agentdeck-state.json', '.agentdeck-process.json'}
        allowed.update(pathlib.Path(r['worktree']['path']).name for r in p['repositories'])
        if set(os.listdir(root)) - allowed:
            raise ValueError('Workspace root contains additional files; move them before removal')
        for entry in p['repositories']:
            run_child(entry, 'check-remove')
        if action == 'remove':
            for entry in p['repositories']:
                check_terminals()
                run_child(entry, 'remove')
            busy()
            # Keep the owned root/receipt as a durable recovery record. The agent
            # may have written root-level files during removal; never rmtree it.
            p['state'] = 'removed'
            p.pop('error', None)
            save()
    print(json.dumps({'workspace': p}))
except (OSError, ValueError, KeyError, TypeError, subprocess.TimeoutExpired) as error:
    result = {'error': str(error)}
    if owned:
        p.update(state='failed', error=str(error))
        try:
            save()
        except (OSError, ValueError) as persistence_error:
            result['error'] += '; could not save workspace progress: ' + str(persistence_error)
        result['workspace'] = p
    print(json.dumps(result))
    sys.exit(1)
finally:
    if lock:
        lock.close()
