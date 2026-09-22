"""Cooperative setup cancellation: only the owning worker signals its children."""
import fcntl,json,os,pathlib,re,signal,stat,subprocess,time

class SetupCancelled(ValueError):
    pass

class SetupControl:
    def __init__(self, plan, create=True):
        token=plan.get('control_token') or plan.get('token','')
        if not re.fullmatch('[0-9a-f]{32}',token) or not os.path.isabs(plan['path']):
            raise ValueError('Invalid setup cancellation identity')
        self.identity={'token':token,'path':os.path.realpath(plan['path']),
                       'repo':os.path.realpath(plan['repo']) if plan['repo'] else ''}
        parent=pathlib.Path(self.identity['path']).parent
        if create:parent.mkdir(parents=True,exist_ok=True)
        self.path=parent/('.lectern-setup-'+token+'.json')
        self.create=create
        self.lease_file=None
        self.file_identity=None
        self.access()

    def access(self, cancel=False, update=None, snapshot=False):
        try:return self._access(cancel,update,snapshot)
        except (OSError,json.JSONDecodeError):raise ValueError('Setup control record is unavailable or unreadable; inspect the allocation') from None

    def _access(self, cancel=False, update=None, snapshot=False):
        fd=os.open(self.path,os.O_RDWR|(os.O_CREAT if self.create else 0)|os.O_NOFOLLOW|os.O_NONBLOCK,0o600)
        with os.fdopen(fd,'r+') as file:
            fcntl.flock(file,fcntl.LOCK_EX)
            info=os.fstat(file.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_nlink!=1:
                raise ValueError('Setup cancellation record is not a private regular file')
            identity=(info.st_dev,info.st_ino)
            if self.file_identity is not None and self.file_identity!=identity:
                raise ValueError('Setup cancellation record was replaced')
            self.file_identity=identity
            current=os.stat(self.path,follow_symlinks=False)
            if (info.st_dev,info.st_ino)!=(current.st_dev,current.st_ino):
                raise ValueError('Setup cancellation record was replaced')
            raw=file.read()
            saved=json.loads(raw) if raw else dict(self.identity,cancelled=False)
            if any(saved.get(k)!=v for k,v in self.identity.items()):
                raise ValueError('Setup cancellation ownership does not match')
            if update:saved.update(update)
            if cancel:saved['cancelled']=True
            if cancel or update or not raw:
                file.seek(0);json.dump(saved,file);file.truncate();file.flush();os.fsync(file.fileno())
            return saved if snapshot else saved.get('cancelled') is True

    def lease(self, create=True):
        try:return self._lease(create)
        except OSError:raise ValueError('Setup operation lock is unavailable; inspect the allocation') from None

    def _lease(self, create=True):
        location=str(self.path)+'.lock'
        fd=os.open(location,os.O_RDWR|(os.O_CREAT if create else 0)|os.O_NOFOLLOW|os.O_NONBLOCK,0o600)
        self.lease_file=os.fdopen(fd,'r+')
        info=os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_nlink!=1:
            raise ValueError('Setup operation lock is not a private regular file')
        try:fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:raise ValueError('A workspace operation is still running; cancel it before recovery')
        identity=[info.st_dev,info.st_ino]
        saved=self.access(snapshot=True)
        if saved.get('operation_lock') is None and create:self.access(update={'operation_lock':identity})
        elif saved.get('operation_lock')!=identity:raise ValueError('Setup operation lock was replaced')
        return fd

    def allocation(self, plan=None):
        if plan is not None:self.access(update={'allocation':plan})
        return self.access(snapshot=True).get('allocation')

    def check(self):
        if self.access():raise SetupCancelled('Workspace setup cancelled; allocated files retained for inspection')

    def wait(self, child, timeout, process_group=None):
        deadline=time.monotonic()+timeout
        try:
            while True:
                self.check()
                remaining=deadline-time.monotonic()
                if remaining<=0:raise subprocess.TimeoutExpired(child.args,timeout)
                try:
                    result=child.communicate(timeout=min(.2,remaining))
                    self.check()
                    return result
                except subprocess.TimeoutExpired:pass
        except BaseException:
            # This Popen is still owned and unreaped, so its PID cannot have been
            # reused. A cancellation requester never signals a recorded PID.
            if child.poll() is None:
                try:os.killpg(process_group or child.pid,signal.SIGKILL)
                except ProcessLookupError:pass
            child.communicate()
            raise
