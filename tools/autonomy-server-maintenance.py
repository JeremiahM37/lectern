#!/usr/bin/python3
"""Fixed, journaled service maintenance. Only the controller supplies authority.

This module never accepts shell commands, unit text, endpoints or file paths from
worker requests. Runtime dispatch supplies a root-owned registry and authority.
An interrupted mutation is reconciled before any new mutation on that service.
"""
import base64
import contextlib
import fcntl
import hashlib
import importlib.util
import json
import os
import pwd
from pathlib import Path
import re
import stat
import subprocess
import sys
import time
import urllib.request
import uuid
from types import SimpleNamespace

_observer_path = Path(__file__).with_name('autonomy-server-operations.py')
if not _observer_path.exists():
    _observer_path = Path(__file__).with_name('lectern-autonomy-server-operations.py')
_spec = importlib.util.spec_from_file_location('lectern_server_observer', _observer_path)
OBS = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(OBS)

ACTION = 'service_resource_limits'
DROPIN = '70-lectern-autonomy-limits.conf'
TERMINAL = {'applied', 'rolled_back', 'conflict', 'rollback_conflict', 'unavailable', 'cancelled'}
MAX_FILE = 1024 * 1024
BOUNDS = OBS.MAINTENANCE_BOUNDS


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()


def sha(value):
    return hashlib.sha256(value).hexdigest()


def key(value):
    return isinstance(value, str) and re.fullmatch('[0-9a-f]{64}', value) is not None


def write_all(fd, raw):
    view = memoryview(raw)
    while view:
        count = os.write(fd,view)
        if count <= 0: raise OSError('short write')
        view = view[count:]


def atomic(path, value):
    path = Path(path)
    raw = canonical(value)
    temp = path.with_name(path.name + '.new')
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    try:
        write_all(fd, raw)
        os.fsync(fd)
    finally:
        os.close(fd)
    os.replace(temp, path)
    fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def root_bytes(path, cap=MAX_FILE):
    fd = OBS.open_path(path)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022 or info.st_size > cap:
            raise ValueError('unsafe trusted file')
        with os.fdopen(fd, 'rb', closefd=False) as stream:
            raw = stream.read(cap + 1)
        if len(raw) > cap:
            raise ValueError('trusted file too large')
        return raw, stat.S_IMODE(info.st_mode)
    finally:
        os.close(fd)


def request_digest(request):
    # Generation is trusted transport authorization, not a new plan or budget.
    return sha(canonical({k: v for k, v in request.items() if k not in ('authority_sha256', 'generation')}))


def verify_sealed(value):
    if not key(value.get('receipt_sha256')) or value['receipt_sha256'] != sha(canonical({k:v for k,v in value.items() if k != 'receipt_sha256'})):
        raise Unavailable('immutable receipt checksum differs')


def limits_text(limits):
    if not isinstance(limits, dict) or set(limits) != {'memory_max_bytes', 'cpu_quota_percent', 'tasks_max'}:
        raise ValueError('exact numeric limits required')
    for name, (low, high) in BOUNDS.items():
        if type(limits[name]) is not int or not low <= limits[name] <= high:
            raise ValueError('limit outside registered maintenance profile')
    return ('[Service]\nMemoryMax=%d\nCPUQuota=%d%%\nTasksMax=%d\n' %
            (limits['memory_max_bytes'], limits['cpu_quota_percent'], limits['tasks_max'])).encode()


def validate(request, authority, registry_sha):
    fields = {'schema_version', 'operation_id', 'target_id', 'service_id', 'action', 'registry_sha256',
              'expected_state_sha256', 'authority_sha256', 'generation', 'limits', 'pin_sha256', 'owner_job', 'owner_task'}
    if not isinstance(request, dict) or set(request) != fields or request['schema_version'] != 1 or request['action'] != ACTION:
        raise ValueError('unsupported maintenance request')
    for name in ('operation_id', 'registry_sha256', 'expected_state_sha256', 'authority_sha256', 'pin_sha256'):
        if not key(request[name]):
            raise ValueError('invalid maintenance identity')
    if type(request['generation']) is not int or request['generation'] < 1:
        raise ValueError('invalid maintenance generation')
    owner_identity(request)
    for name in ('target_id', 'service_id'):
        if not isinstance(request[name], str) or not re.fullmatch('[a-z][a-z0-9_-]{0,63}', request[name]):
            raise ValueError('invalid registered resource ID')
    if request['registry_sha256'] != registry_sha or sha(canonical(authority)) != request['authority_sha256']:
        raise ValueError('trusted registry or authority differs')
    for name in ('operation_id', 'target_id', 'service_id', 'registry_sha256', 'expected_state_sha256', 'pin_sha256', 'owner_job', 'owner_task'):
        if authority.get(name) != request[name]:
            raise ValueError('authority scope differs')
    if authority.get('request_sha256') != request_digest(request) or authority.get('action') != ACTION:
        raise ValueError('authority request differs')
    audits = authority.get('audit_receipt_sha256')
    if not isinstance(audits, dict) or set(audits) != {'auditor_a', 'auditor_b'} or not all(key(v) for v in audits.values()) or len(set(audits.values())) != 2:
        raise ValueError('two independent audit bindings required')
    if not key(authority.get('candidate_review_sha256')):
        raise ValueError('independent candidate review required')
    if not key(authority.get('backup_receipt_sha256')):
        raise ValueError('off-box backup receipt binding required')
    if not key(authority.get('validation_receipt_sha256')):
        raise ValueError('executed candidate validation binding required')
    return limits_text(request['limits'])


def owner_identity(value):
    try:
        valid = str(uuid.UUID(value['owner_job'])) == value['owner_job']
    except (ValueError, TypeError, KeyError):
        valid = False
    if not valid or type(value.get('owner_task')) is not int or value['owner_task'] <= 0:
        raise ValueError('admitted owner required')


class Unavailable(Exception):
    pass


class Executor:
    """Backend is trusted fixed implementation; tests substitute a disposable one."""
    def __init__(self, root, backend):
        self.root = Path(root)
        self.backend = backend
        self.root.mkdir(parents=True, exist_ok=True, mode=0o700)

    def _save(self, path, journal, phase):
        if phase in ('apply_intent','applied_pending_health','rollback_intent'):
            journal['effects_started'] = True
        journal['phase'] = phase
        journal['updated_at'] = time.time()
        atomic(path, journal)

    def _finish(self, path, journal, state, reason=''):
        receipt = {k: journal[k] for k in ('operation_id', 'request_sha256', 'authority_sha256', 'before_sha256', 'candidate_sha256')}
        receipt.update(applied_state_sha256=journal.get('applied_state_sha256'), restored_sha256=journal.get('restored_sha256'), invocation_id=journal.get('invocation_id'), candidate_dropin_sha256=journal.get('candidate_dropin_sha256'), schema_version=1, state=state, reason=reason, backup=journal.get('backup'),
                       observations=journal.get('observations', []), generation=journal['generation'])
        receipt['effects_started'] = journal.get('effects_started')
        receipt['no_effects'] = journal.get('effects_started') is False and state in ('cancelled','unavailable','conflict')
        receipt['helper_sha256'] = sha(Path(__file__).read_bytes())
        receipt['receipt_sha256'] = sha(canonical(receipt))
        journal['receipt'] = receipt
        self._save(path, journal, state)
        return receipt

    def cancel(self, operation_id, generation):
        if not key(operation_id) or type(generation) is not int or generation < 1:
            raise ValueError('invalid cancellation')
        # Separate cancellation lock permits cancellation during bounded health
        # checks without waiting for the entire service transaction lock.
        with open(self.root / (operation_id + '.cancel.lock'), 'a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            path = self.root / (operation_id + '.cancel.json')
            old = json.loads(path.read_bytes()) if path.exists() else {}
            atomic(path, {'revoked_through': max(generation, old.get('revoked_through', 0))})

    def _cancelled(self, journal):
        path = self.root / (journal['operation_id'] + '.cancel.json')
        return path.exists() and json.loads(path.read_bytes())['revoked_through'] >= journal['generation']

    def prepare_backup(self, request):
        """Trusted controller calls this read-only stage before final reservation."""
        if not key(request.get('operation_id')) or request.get('registry_sha256') != self.backend.registry_sha:
            raise ValueError('backup identity differs')
        limits_text(request['limits'])
        resource = self.backend.resolve(request['target_id'], request['service_id'])
        directory = self.root / (request['operation_id'] + '.backup')
        directory.mkdir(mode=0o700, exist_ok=True)
        with open(directory / 'lock', 'a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            before = self.backend.capture(resource)
            digest = self.backend.state_sha(before)
            if digest != request['expected_state_sha256']:
                raise Unavailable('backup before-state changed')
            path = directory / 'proof.json'
            if path.exists():
                proof = json.loads(path.read_bytes())
                verify_sealed(proof)
                if proof['before_sha256'] != digest:
                    raise ValueError('backup operation identity changed')
                return proof
            self.backend.preflight(resource, request['limits'])
            journal = dict(operation_id=request['operation_id'], before=before, before_sha256=digest)
            proof = self.backend.backup(resource, journal, directory)
            if proof.get('state') != 'offbox_restored_verified' or proof.get('before_sha256') != digest:
                raise Unavailable('off-box restore proof unavailable')
            proof['receipt_sha256'] = sha(canonical(proof))
            atomic(path, proof)
            return proof

    def validate_candidate(self, request):
        """Execute only the registered synthetic workload, never target code."""
        if not key(request.get('operation_id')) or request.get('registry_sha256') != self.backend.registry_sha:
            raise ValueError('candidate identity differs')
        owner_identity(request)
        if not key(request.get('pin_sha256')):
            raise ValueError('candidate admission pin missing')
        text = limits_text(request['limits'])
        resource = self.backend.resolve(request['target_id'], request['service_id'])
        before = self.backend.capture(resource)
        if self.backend.state_sha(before) != request['expected_state_sha256']:
            raise Unavailable('candidate before-state changed')
        directory = self.root / (request['operation_id'] + '.validation')
        directory.mkdir(mode=0o700, exist_ok=True)
        with open(directory / 'lock', 'a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            identity = dict(operation_id=request['operation_id'], registry_sha256=self.backend.registry_sha,
                            before_sha256=request['expected_state_sha256'], candidate_sha256=sha(canonical(request['limits'])),
                            candidate_dropin_sha256=sha(text), pin_sha256=request['pin_sha256'],
                            owner_task=request['owner_task'], owner_job=request['owner_job'])
            path = directory / 'receipt.json'
            if path.exists():
                receipt = json.loads(path.read_bytes())
                verify_sealed(receipt)
                if any(receipt.get(k) != v for k,v in identity.items()):
                    raise ValueError('immutable candidate validation changed')
                return receipt
            # A crash cannot silently run a second fresh validation budget.
            intent = directory / 'intent.json'
            history = []
            if intent.exists():
                previous = json.loads(intent.read_bytes())
                if previous['identity'] != identity:
                    raise ValueError('validation admission changed')
                if time.time() - previous['started_at'] < 60:
                    raise Unavailable('interrupted validation cooldown; retained same reservation')
                if previous['attempt'] >= 3:
                    raise Unavailable('bounded validation attempts exhausted')
                if not self.backend.validation_stopped(Path(previous['directory'])):
                    raise Unavailable('prior validation still owns its execution slot')
                history = previous['history'] + [dict(attempt=previous['attempt'], state='interrupted', reserved_ms=45000)]
            attempt = len(history) + 1
            stage = directory / ('attempt-' + str(attempt))
            stage.mkdir(mode=0o700, exist_ok=True)
            atomic(intent, dict(identity=identity, attempt=attempt, directory=str(stage), started_at=time.time(), history=history))
            if self._cancelled({'operation_id':request['operation_id'],'generation':request['generation']}):
                raise Unavailable('candidate validation revoked')
            self.backend.validation_cancelled = lambda: self._cancelled({'operation_id':request['operation_id'],'generation':request['generation']})
            result = self.backend.validate_candidate(resource, request['limits'], stage)
            receipt = dict(identity, schema_version=1, state='validated' if result.get('executed') is True and result.get('exit_code') == 0 else 'validation_failed',
                           executed=result.get('executed') is True, evidence=result,
                           attempts=history + [dict(attempt=attempt, state='executed' if result.get('executed') else 'not_executed', charged_ms=result.get('elapsed_ms'))],
                           exit_code=result.get('exit_code'), output_sha256=result.get('output_sha256'),
                           profile=OBS.MAINTENANCE_PROFILE, profile_sha256=sha(canonical({'profile':OBS.MAINTENANCE_PROFILE,'helper_sha256':sha(Path(__file__).read_bytes()),'limits':request['limits']})),
                           scope='fixed synthetic sensor workload; production target unchanged', mutation_performed=False,
                           helper_sha256=sha(Path(__file__).read_bytes()))
            receipt['receipt_sha256'] = sha(canonical(receipt))
            atomic(path, receipt)
            return receipt

    def execute(self, request, authority):
        candidate = validate(request, authority, self.backend.registry_sha)
        resource = self.backend.resolve(request['target_id'], request['service_id'])
        # One transaction lock per registered physical service, not per alias.
        resource_id = sha(canonical(resource['identity']))
        with open(self.root / (resource_id + '.lock'), 'a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            return self._execute_locked(resource, request, candidate, authority)

    def _external_generation_inspected(self, old, before, request, resource):
        # A positive read-only inspection releases only the old conflict. The new
        # operation still needs its own complete authority and exact before CAS.
        # Later external changes do not erase this historical release proof.
        if not key(old.get('operation_id')) or old.get('phase') != 'rollback_conflict':
            return False
        try:
            verify_sealed(old['receipt'])
            old_registry=old.get('registry_sha256')
            if not key(old_registry):old_registry=sha(root_bytes(self.root.parent/'operations'/old['operation_id']/'registry.json',OBS.MAX_REGISTRY)[0])
            inspections = self.root.parent / 'operations' / old['operation_id'] / 'inspections'
            paths = sorted(inspections.glob('*/receipt.json'), key=lambda p:p.stat().st_mtime, reverse=True)[:64]
            for path in paths:
                try:
                    outer=json.loads(root_bytes(path,256*1024)[0]);verify_sealed(outer)
                    proof=outer.get('result',{});verify_sealed(proof)
                    if (outer.get('state')!='external_healthy' or outer.get('phase')!='inspect' or outer.get('operation_id')!=old['operation_id'] or not key(outer.get('inspection_id')) or proof.get('inspection_id')!=outer['inspection_id'] or
                        proof.get('state')!='external_healthy' or proof.get('operation_id')!=old['operation_id'] or proof.get('request_sha256')!=old['request_sha256'] or proof.get('authority_sha256')!=old['authority_sha256'] or
                        proof.get('conflict_receipt_sha256')!=old['receipt']['receipt_sha256'] or proof.get('registry_sha256')!=old_registry or proof.get('before_sha256')!=old['before_sha256'] or proof.get('candidate_sha256')!=old['candidate_sha256'] or
                        proof.get('profile')!='registered_service_external_health_v1' or proof.get('no_mutation') is not True or proof.get('owned_candidate') is not False or
                        not key(proof.get('current_state_sha256')) or proof.get('post_state_sha256')!=proof.get('current_state_sha256') or not re.fullmatch('[a-f0-9]{32}',proof.get('invocation_id','')) or proof.get('post_invocation_id')!=proof.get('invocation_id')):
                        continue
                    observations=proof.get('observations',[])
                    if len(observations)!=3 or not all(o.get('healthy') is True and key(o.get('response_sha256')) and key(o.get('metrics_sha256')) for o in observations):continue
                    return True
                except (OSError,ValueError,KeyError,TypeError):
                    continue
        except (OSError,ValueError,KeyError,TypeError):
            pass
        return False

    def _execute_locked(self, resource, request, candidate, authority):
        path = self.root / (request['operation_id'] + '.json')
        digest = request_digest(request)
        if path.exists():
            j = json.loads(path.read_bytes())
            if j['request_sha256'] != digest or j['authority_sha256'] != request['authority_sha256']:
                raise ValueError('immutable operation changed')
            if request['generation'] < j['generation']:
                raise Unavailable('stale revoked generation')
            if request['generation'] > j['generation']:
                if j['phase'] not in ('cancelled','unavailable','conflict') or j.get('effects_started') is not False or j.get('receipt',{}).get('no_effects') is not True:
                    raise Unavailable('only proven pre-effect terminal operation may resume')
                j.setdefault('prior_receipts', []).append(j.pop('receipt'))
                j['generation'] = request['generation']
                self._save(path,j,'reserved')
            if j['phase'] in TERMINAL:
                if j['phase'] == 'applied' and self._cancelled(j):
                    j['apply_receipt'] = j['receipt']
                    return self._rollback(resource, path, j, 'revoked after verified application')
                return j['receipt']
            if j['phase'] in ('apply_intent', 'applied_pending_health', 'rollback_intent'):
                return self._rollback(resource, path, j, 'interrupted transaction reconciled')
        else:
            before = self.backend.capture(resource)
            j = dict(operation_id=request['operation_id'], registry_sha256=request['registry_sha256'], request_sha256=digest,
                     authority_sha256=request['authority_sha256'], backup_receipt_sha256=authority['backup_receipt_sha256'], validation_receipt_sha256=authority['validation_receipt_sha256'], generation=request['generation'],
                     before=before, before_sha256=self.backend.state_sha(before), candidate_sha256=sha(canonical(request['limits'])), candidate_dropin_sha256=sha(candidate), resource_identity=resource['identity'],
                     candidate=base64.b64encode(candidate).decode(), observations=[], effects_started=False)
            self._save(path, j, 'reserved')
        # A prior unfinished operation on this same service cannot be bypassed
        # by changing operation ID. Its immutable intent must be reconciled first.
        for other in self.root.glob('*.json'):
            if other == path or other.name.endswith('.cancel.json'):
                continue
            old = json.loads(other.read_bytes())
            if old.get('resource_identity') == resource['identity'] and old.get('phase') == 'rollback_conflict' and not self._external_generation_inspected(old,j['before'],request,resource):
                return self._finish(path,j,'conflict','retained external conflict requires authenticated healthy inspection')
            if old.get('resource_identity') == resource['identity'] and old.get('phase') not in TERMINAL:
                if old.get('phase') in ('apply_intent', 'applied_pending_health', 'rollback_intent'):
                    self._rollback(resource, other, old, 'reconciled before later operation')
                return self._finish(path, j, 'conflict', 'prior operation must be reconciled before fresh observation')
        j['resource_identity'] = resource['identity']
        self._save(path, j, j['phase'])
        if self._cancelled(j):
            return self._finish(path, j, 'cancelled', 'revoked before mutation')
        if j['before_sha256'] != request['expected_state_sha256'] or self.backend.capture(resource) != j['before']:
            return self._finish(path, j, 'conflict', 'stable before-state changed')
        try:
            self.backend.preflight(resource, request['limits'])
            validation_path = self.root / (j['operation_id'] + '.validation') / 'receipt.json'
            if not validation_path.exists():
                raise Unavailable('executed candidate validation missing')
            validation = json.loads(validation_path.read_bytes())
            verify_sealed(validation)
            if validation.get('receipt_sha256') != j['validation_receipt_sha256'] or validation.get('state') != 'validated' or validation.get('executed') is not True or validation.get('before_sha256') != j['before_sha256'] or validation.get('candidate_sha256') != j['candidate_sha256'] or validation.get('registry_sha256') != request['registry_sha256'] or validation.get('pin_sha256') != request['pin_sha256']:
                raise Unavailable('candidate validation differs from authority')
            if validation.get('owner_job') != authority.get('validation_owner_job') or validation.get('owner_task') != authority.get('validation_owner_task') or validation.get('owner_job') == request['owner_job'] or validation.get('owner_task') == request['owner_task']:
                raise Unavailable('independent validation owner differs')
            if j.get('backup') is None:
                proof_path = self.root / (j['operation_id'] + '.backup') / 'proof.json'
                if not proof_path.exists():
                    raise Unavailable('off-box preparation receipt missing')
                j['backup'] = json.loads(proof_path.read_bytes())
                verify_sealed(j['backup'])
                if j['backup'].get('state') != 'offbox_restored_verified' or j['backup'].get('before_sha256') != j['before_sha256']:
                    raise Unavailable('off-box restore proof unavailable')
                if j['backup'].get('receipt_sha256') != j['backup_receipt_sha256']:
                    raise Unavailable('admitted backup differs')
                self._save(path, j, 'backup_verified')
            if self._cancelled(j):
                return self._finish(path, j, 'cancelled', 'revoked before mutation')
            if self.backend.capture(resource) != j['before']:
                return self._finish(path, j, 'conflict', 'before-state changed during backup')
            self.backend.preflight(resource, request['limits'])
            self._save(path, j, 'apply_intent')
            self.backend.install(resource, candidate, j['before'])
            self._save(path, j, 'applied_pending_health')
            self.backend.activate(resource)
            j['observations'] = self.backend.health(resource, request['limits'], lambda: self._cancelled(j))
            if self._cancelled(j):
                return self._rollback(resource, path, j, 'revoked after mutation')
            if not j['observations'] or not all(o.get('healthy') is True for o in j['observations']):
                return self._rollback(resource, path, j, 'post-apply health failed')
            if not self.backend.owns(resource, j['before'], candidate):
                return self._finish(path, j, 'rollback_conflict', 'foreign change after activation; no overwrite')
            j['applied_state_sha256'] = self.backend.state_sha(self.backend.capture(resource))
            j['invocation_id'] = self.backend.invocation(resource)
            return self._finish(path, j, 'applied')
        except Exception as exc:
            # Exception text from systemd/Restic may include sensitive paths;
            # preserve the classification, never raw command output.
            if j['phase'] in ('apply_intent', 'applied_pending_health', 'rollback_intent'):
                return self._rollback(resource, path, j, 'activation or health error: ' + type(exc).__name__)
            return self._finish(path, j, 'unavailable', 'precondition unavailable: ' + type(exc).__name__)

    def _rollback(self, resource, path, j, reason):
        candidate = base64.b64decode(j['candidate'], validate=True)
        # Crashes immediately before install, or after restoration, are safe.
        if self.backend.same_files(self.backend.capture(resource), j['before']):
            self._save(path, j, 'rollback_intent')
            self.backend.restore_directory(resource, j['before'])
            if self.backend.state_sha(self.backend.capture(resource)) != j['before_sha256']:
                self.backend.activate(resource)
            return self._restored(resource, path, j, reason)
        if not self.backend.owns(resource, j['before'], candidate):
            return self._finish(path, j, 'rollback_conflict', 'foreign change prevents automatic restoration')
        self._save(path, j, 'rollback_intent')
        self.backend.restore(resource, j['before'], candidate)
        self.backend.activate(resource)
        if self.backend.state_sha(self.backend.capture(resource)) != j['before_sha256']:
            raise Unavailable('restoration must be reconciled again')
        return self._restored(resource, path, j, reason)

    def _restored(self, resource, path, j, reason):
        j['observations'] = self.backend.restored_health(resource)
        if not j['observations'] or not all(o.get('healthy') is True for o in j['observations']):
            raise Unavailable('restored service health not established; retain rollback ownership')
        j['restored_sha256'] = self.backend.state_sha(self.backend.capture(resource))
        if j['restored_sha256'] != j['before_sha256']:
            raise Unavailable('restored stable configuration differs')
        return self._finish(path, j, 'rolled_back', reason)


class SystemdBackend:
    """Local-only fixed adapter. Registry is supplied by trusted root dispatch."""
    def __init__(self, registry, registry_sha):
        self.registry, self.registry_sha = registry, registry_sha

    def resolve(self, target_id, service_id):
        target = self.registry['targets'][target_id]
        service = target['services'][service_id]
        entries = [v for v in self.registry.get('maintenance', {}).values() if v.get('target_id') == target_id and v.get('service_id') == service_id]
        if target['kind'] != 'local_systemd' or len(entries) != 1:
            raise ValueError('maintenance not registered')
        registration = entries[0]
        if set(registration) != {'target_id','service_id','action','health_id','backup_profile','stateless','protected'} or registration['action'] != ACTION or registration['stateless'] is not True or registration['protected'] is not False:
            raise ValueError('unsupported maintenance registration')
        unit = service['unit']
        if not re.fullmatch(r'[A-Za-z0-9_-]+\.service', unit):
            raise ValueError('unsupported service unit')
        protected = OBS.MAINTENANCE_PROTECTED
        if unit in protected:
            raise ValueError('session or control-plane service is protected')
        directory = Path('/etc/systemd/system') / (unit + '.d')
        if service.get('dropin_directory') != str(directory):
            raise ValueError('registered drop-in directory differs')
        health = target.get('health', {}).get(registration['health_id'])
        if not health or health.get('adapter') != 'temp_api_v1':
            raise ValueError('registered health profile unavailable')
        return dict(identity={'unit': unit}, service=service, target_id=target_id, service_id=service_id,
                    maintenance=registration, health=health, unit=unit, dropin=directory / DROPIN)

    @staticmethod
    def state_sha(before):
        return before['configuration_sha256']

    @staticmethod
    def same_files(a,b):
        return a['files'] == b['files']

    def invocation(self,r):
        value = self.properties(r,['InvocationID']).get('InvocationID','')
        if not re.fullmatch('[a-f0-9]{32}',value):
            raise Unavailable('service invocation missing')
        return value

    @staticmethod
    def command(args, timeout=30, env=None):
        # Observer's fixed subprocess supervisor bounds time/output and kills
        # the whole owned subprocess group on timeout, including SSH children.
        return OBS.bounded_command(args, seconds=timeout, limit=2*1024**2)

    def properties(self, r, names):
        raw = self.command(['systemctl', 'show', r['unit'], *['--property=' + n for n in names]])
        return dict(line.split('=', 1) for line in raw.decode().splitlines() if '=' in line)

    def capture(self, r):
        files = {}
        paths = [item['path'] for item in r['service']['identity_files']]
        directory = r['dropin'].parent
        if directory.exists():
            if directory.is_symlink() or len(list(directory.iterdir())) > 32:
                raise Unavailable('unsafe drop-in inventory')
            paths += [str(p) for p in sorted(directory.iterdir())]
        for name in sorted(set(paths)):
            raw, mode = root_bytes(name)
            files[name] = dict(sha256=sha(raw), mode=mode, bytes=base64.b64encode(raw).decode())
        observation = OBS.collect(self.registry, self.registry_sha, {'schema_version':1,'request_id':'0'*64,'owner_job':'00000000-0000-4000-8000-000000000000','owner_task':1,'target_id':r['target_id'],'registry_sha256':self.registry_sha})
        if not observation['configuration_complete']:
            raise Unavailable('stable configuration incomplete')
        properties = self.properties(r, ['FragmentPath', 'DropInPaths', 'MemoryMax', 'TasksMax', 'CPUQuotaPerSecUSec'])
        if properties.get('FragmentPath') not in files or len(r['service']['identity_files']) < 2:
            raise Unavailable('unit and launcher identities must be registered')
        return {'invocation_id':self.invocation(r),'configuration_sha256':observation['configuration_sha256'],'directory_absent':not directory.exists(), 'files': files, 'dropin': files.get(str(r['dropin'])),
                'properties': properties}

    def preflight(self, r, limits):
        p = self.properties(r, ['ActiveState', 'SubState', 'MemoryCurrent', 'TasksCurrent'])
        if p.get('ActiveState') != 'active' or p.get('SubState') != 'running':
            raise Unavailable('service not healthy before mutation')
        if max(int(p['MemoryCurrent']) * OBS.MAINTENANCE_HEADROOM_RATIO, int(p['MemoryCurrent']) + OBS.MAINTENANCE_HEADROOM_BYTES) > limits['memory_max_bytes'] or int(p['TasksCurrent']) * 2 > limits['tasks_max']:
            raise Unavailable('live resource headroom insufficient')
        if not self._health_once(r)['healthy']:
            raise Unavailable('baseline endpoint unhealthy')

    def validate_candidate(self, r, limits, directory):
        # Only this trusted module's fixed workload executes. No registry or
        # worker-selected executable, shell, source code, file or URL is used.
        helper = Path(__file__).resolve()
        root_bytes(helper, 256*1024)
        unit = self.validation_unit(directory)
        options = ['MemoryMax=' + str(limits['memory_max_bytes']),
                   'CPUQuota=' + str(limits['cpu_quota_percent']) + '%',
                   'TasksMax=' + str(limits['tasks_max']), 'DynamicUser=yes',
                   'PrivateNetwork=yes', 'PrivateTmp=yes', 'NoNewPrivileges=yes',
                   'ProtectSystem=strict', 'ProtectHome=yes', 'ProtectControlGroups=yes',
                   'RestrictSUIDSGID=yes', 'CapabilityBoundingSet=', 'RuntimeMaxSec=20s',
                   'TimeoutStopSec=3s', 'MemorySwapMax=0']
        # verify performs no activation. The generated unit contains only fixed
        # executable paths plus validated integers, never model directives.
        probe = directory / (unit + '.service')
        probe.write_text('[Unit]\nDescription=Lectern isolated candidate validation\n[Service]\nType=exec\nExecStart=/usr/bin/true\n' + '\n'.join(options) + '\n')
        os.chmod(probe, 0o600)
        self.command(['/usr/bin/systemd-analyze', 'verify', str(probe)], timeout=15)
        started = time.monotonic()
        try:
            guard_path = getattr(self,'validation_guard',None)
            with (guard_path.open('a') if guard_path else contextlib.nullcontext()) as guard:
                if guard is not None:fcntl.flock(guard,fcntl.LOCK_EX)
                if getattr(self,'validation_cancelled',lambda:False)():
                    raise Unavailable('validation revoked before owned launch')
                output = self.command(['/usr/bin/systemd-run', '--quiet', '--wait', '--pipe', '--collect',
                                       '--unit=' + unit, *['--property=' + value for value in options],
                                       '--', '/usr/bin/python3', '-I', '-S', str(helper), '--candidate-workload',
                                       str(limits['memory_max_bytes']), str(limits['cpu_quota_percent']), str(limits['tasks_max'])], timeout=30)
            result = json.loads(output, object_pairs_hook=OBS.unique)
            if result.get('profile') != 'synthetic_sensor_v1' or result.get('limits') != limits or result.get('healthy') is not True:
                raise Unavailable('candidate workload evidence differs')
            return dict(executed=True, exit_code=0, elapsed_ms=int((time.monotonic()-started)*1000), workload=result,
                        output_sha256=sha(output), unit_verified=True)
        finally:
            # Only the exact disposable unit created above may be stopped.
            try:
                self.command(['/usr/bin/systemctl', 'stop', unit + '.service'], timeout=5)
            except Exception:
                pass

    @staticmethod
    def validation_unit(directory):
        return 'lectern-maintenance-validation-' + sha(str(directory).encode())

    def validation_stopped(self, directory):
        raw = self.command(['/usr/bin/systemctl','show',self.validation_unit(directory)+'.service','--property=ActiveState','--value'],timeout=5)
        return raw.strip() in (b'inactive',b'failed')

    def backup(self, r, j, directory):
        profile = r['maintenance'].get('backup_profile')
        if profile not in self.registry.get('backup_profiles', {}):
            raise Unavailable('registered off-box profile missing')
        config = self.registry['backup_profiles'][profile]
        if set(config) != {'repository_file','password_file','offbox_host'}:
            raise Unavailable('unsupported backup profile')
        repository = root_bytes(config['repository_file'], 4096)[0].decode().strip()
        # Only registered SFTP destinations; never a local repository labelled
        # off-box by the request. Root registration defines the remote host.
        match = re.fullmatch(r'sftp:(?:[A-Za-z0-9_-]+@)?([A-Za-z0-9_.-]+):(/[^\n]+)', repository)
        host = config['offbox_host']
        if not match or match[1] != host or host in ('localhost', '127.0.0.1', '::1', os.uname().nodename):
            raise Unavailable('off-box destination differs from registration')
        root_bytes(config['password_file'], 16384)  # ownership only; never output
        directory.mkdir(mode=0o700, exist_ok=True)
        source = directory / 'source'
        source.mkdir(mode=0o700, exist_ok=True)
        atomic(source / 'before.json', j['before'])
        manifest = sha((source / 'before.json').read_bytes())
        common = ['restic', '--repo', repository, '--password-file', config['password_file']]
        env = {'PATH': '/usr/local/bin:/usr/bin:/bin', 'HOME': '/root'}
        repository_config = json.loads(self.command(common + ['cat', 'config'], timeout=30, env=env))
        repository_id = repository_config.get('id')
        if not key(repository_id):
            raise Unavailable('off-box repository identity missing')
        output = self.command(common + ['backup', str(source), '--json', '--tag', 'lectern-maintenance-' + j['operation_id']], timeout=300, env=env)
        records = [json.loads(line) for line in output.splitlines()]
        snapshot = next((x.get('snapshot_id') for x in reversed(records) if x.get('message_type') == 'summary'), None)
        if not key(snapshot):
            raise Unavailable('exact off-box snapshot missing')
        restored = directory / 'restored'
        restored.mkdir(mode=0o700, exist_ok=True)
        self.command(common + ['restore', snapshot, '--target', str(restored)], timeout=300, env=env)
        restored_file = restored / str(source).lstrip('/') / 'before.json'
        content, mode = root_bytes(restored_file)
        if sha(content) != manifest or mode != 0o600:
            raise Unavailable('off-box restored content differs')
        return dict(state='offbox_restored_verified', before_sha256=j['before_sha256'], snapshot_id=snapshot, repository_id=repository_id, repository_sha256=sha(repository.encode()), offbox_receipt_sha256=sha(canonical({'snapshot_id':snapshot,'repository_id':repository_id,'profile_sha256':sha(canonical(config))})), restore_proof_sha256=sha(canonical({'snapshot_id':snapshot,'restored_sha256':manifest,'mode':mode})),
                    profile_sha256=sha(canonical(config)), restored_manifest_sha256=manifest)

    def install(self, r, candidate, before):
        if self.capture(r) != before:
            raise Unavailable('before-state changed')
        parent = r['dropin'].parent
        parent.mkdir(mode=0o755, exist_ok=True)
        if parent.is_symlink() or parent.stat().st_uid != 0 or parent.stat().st_mode & 0o022:
            raise Unavailable('unsafe service drop-in directory')
        self._replace(r['dropin'], candidate, 0o644)

    @staticmethod
    def _replace(path, raw, mode):
        temp = path.parent.parent / ('.lectern-' + sha(str(path).encode()) + '-' + sha(raw) + '.tmp')
        if temp.exists():
            if root_bytes(temp)[0] != raw:
                raise Unavailable('staged replacement differs')
            os.replace(temp,path)
            return
        fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
        try:
            write_all(fd, raw)
            os.fchmod(fd, mode)
            os.fsync(fd)
        finally:
            os.close(fd)
        os.replace(temp, path)
        fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    def owns(self, r, before, candidate):
        current = self.capture(r)
        # Effective limits legitimately differ after reload; file inventory is
        # the CAS authority. Every other registered file must remain identical.
        expected = dict(before['files'])
        expected[str(r['dropin'])] = dict(sha256=sha(candidate), mode=0o644, bytes=base64.b64encode(candidate).decode())
        return current['files'] == expected

    def restore(self, r, before, candidate):
        if not self.owns(r, before, candidate):
            raise Unavailable('rollback ownership changed')
        old = before['dropin']
        if old is None:
            r['dropin'].unlink()
            fd = os.open(r['dropin'].parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
        else:
            self._replace(r['dropin'], base64.b64decode(old['bytes'], validate=True), old['mode'])
        self.restore_directory(r,before)

    def restore_directory(self, r, before):
        if before.get('directory_absent') and r['dropin'].parent.exists():
            r['dropin'].parent.rmdir()  # only an empty directory created by this transaction

    def activate(self, r):
        self.command(['systemctl', 'daemon-reload'])
        self.command(['systemctl', 'restart', r['unit']], timeout=30)

    def _health_once(self, r):
        # Initial registry profile: fixed sensor service schema, no caller URL.
        if r['health']['adapter'] != 'temp_api_v1':
            raise Unavailable('unsupported fixed health profile')
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *args, **kwargs):
                raise Unavailable('health redirect refused')
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        with opener.open('http://127.0.0.1:' + str(r['health']['port']) + '/', timeout=3) as response:
            raw = response.read(65537)
        if len(raw) > 65536:
            raise Unavailable('health body exceeds bound')
        data = json.loads(raw)
        with opener.open('http://127.0.0.1:' + str(r['health']['port']) + '/metrics', timeout=3) as response:
            metrics = response.read(65537).decode()
        numeric = OBS.temp_health(r['health'])
        healthy = (numeric['state'] == 'available' and len(metrics) <= 65536 and type(data.get('_cpu_percent')) in (int, float) and
                   0 <= data['_cpu_percent'] <= 100 and type(data.get('_cores')) is int and data['_cores'] > 0 and
                   any(not k.startswith('_') and type(v) in (int, float) for k, v in data.items()) and
                   'homelab_temp_celsius{' in metrics and 'homelab_cpu_percent ' in metrics)
        return dict(healthy=healthy, observed_at=time.time(), response_sha256=sha(raw), metrics_sha256=sha(metrics.encode()))

    def health(self, r, limits, cancelled):
        observations = []
        self._wait_ready(r,cancelled)
        for _ in range(3):
            if cancelled():
                break
            observation = self._health_once(r)
            p = self.properties(r, ['ActiveState', 'SubState', 'MemoryMax', 'TasksMax', 'CPUQuotaPerSecUSec'])
            # systemd's time-format presentation is independently recorded;
            # memory/task properties must equal the requested effective limits.
            observation['healthy'] = observation['healthy'] and p.get('ActiveState') == 'active' and p.get('SubState') == 'running' and p.get('MemoryMax') == str(limits['memory_max_bytes']) and p.get('TasksMax') == str(limits['tasks_max']) and OBS.duration(p.get('CPUQuotaPerSecUSec','')) == limits['cpu_quota_percent'] * 10000
            observation['properties'] = p
            observations.append(observation)
            time.sleep(3)
        return observations


    def _wait_ready(self,r,cancelled=lambda:False):
        deadline = time.monotonic() + 15
        while True:
            if cancelled():
                raise Unavailable('cancelled during startup readiness')
            try:
                observation = self._health_once(r)
                if observation['healthy']:
                    return observation
            except Exception:
                pass
            if time.monotonic() >= deadline:
                raise Unavailable('bounded startup readiness failed')
            time.sleep(.2)

    def restored_health(self,r):
        return [self._wait_ready(r)]

def execute_trusted(root, registry_path, request_path, authority_path):
    """Dispatch must choose these paths; no worker path parameter is exposed."""
    root = Path(root)
    root.mkdir(parents=True, mode=0o700, exist_ok=True)
    fd = OBS.open_path(root, directory=True)
    try:
        info = os.fstat(fd)
        if info.st_uid != 0 or stat.S_IMODE(info.st_mode) & 0o077:
            raise ValueError('transaction root must be root-private')
    finally:
        os.close(fd)
    registry, registry_sha = OBS.load_registry(registry_path)
    request_raw, _ = root_bytes(request_path, 16384)
    authority_raw, _ = root_bytes(authority_path, 16384)
    backend = SystemdBackend(registry, registry_sha)
    return Executor(root, backend).execute(json.loads(request_raw, object_pairs_hook=OBS.unique), json.loads(authority_raw, object_pairs_hook=OBS.unique))


R = None
PHASES = {'validate','backup','apply','reconcile'}


def private_directory(path):
    path = Path(path)
    path.mkdir(mode=0o700, exist_ok=True)
    fd = OBS.open_path(path, directory=True)
    try:
        info = os.fstat(fd)
        if info.st_uid != 0 or info.st_mode & 0o077:
            raise ValueError('unsafe maintenance state directory')
    finally:
        os.close(fd)
    return path


def operation_root(operation):
    if not key(operation):
        raise ValueError('invalid operation')
    private_directory(R.SERVER_MAINTENANCE_ROOT)
    parent = private_directory(R.SERVER_MAINTENANCE_ROOT/'operations')
    return private_directory(parent/operation)


def transaction_root():
    private_directory(R.SERVER_MAINTENANCE_ROOT)
    return private_directory(R.SERVER_MAINTENANCE_ROOT/'transactions')


def stage_path(operation, phase, job, generation):
    R.job_path(job)
    if phase not in PHASES or type(generation) is not int or generation < 1:
        raise ValueError('invalid maintenance phase/generation')
    root = operation_root(operation)
    for component in ('phases',phase,job,str(generation)):
        root = private_directory(root/component)
    return root


def frozen_file(path, raw, mode=0o400):
    if path.exists():
        existing, actual_mode = root_bytes(path, 1024*1024)
        if existing != raw or actual_mode != mode:
            raise ValueError('immutable maintenance input changed')
        return
    pending = path.with_name(path.name+'.pending')
    fd = os.open(pending,os.O_WRONLY|os.O_CREAT|os.O_TRUNC|os.O_NOFOLLOW,mode)
    try:
        write_all(fd,raw);os.fchmod(fd,mode);os.fsync(fd)
    finally:
        os.close(fd)
    os.replace(pending,path)
    fd=os.open(path.parent,os.O_RDONLY|os.O_DIRECTORY)
    try:os.fsync(fd)
    finally:os.close(fd)


def controller_bytes(path):
    fd=OBS.open_path(path)
    try:
        info=os.fstat(fd)
        if info.st_uid not in (0,pwd.getpwnam('admin').pw_uid) or info.st_mode&0o077:
            raise ValueError('maintenance input must be private controller-owned file')
        return OBS.read_descriptor(fd,16384)[0]
    finally:os.close(fd)


def freeze_executables(operation):
    root=operation_root(operation)
    manifest=root/'executables.json'
    if manifest.exists():
        selected=json.loads(root_bytes(manifest)[0])
        if not key(selected.get('digest')):raise ValueError('invalid executable identity')
        return R.SERVER_MAINTENANCE_TOOLS/selected['digest']
    sources={'autonomy-server-maintenance.py':R.SERVER_MAINTENANCE_HELPER,
             'autonomy-server-operations.py':R.SERVER_OPERATIONS_HELPER,
             'runner.py':Path(R.__file__)}
    images={name:root_bytes(source,1024*1024)[0] for name,source in sources.items()}
    digest=sha(canonical({name:sha(raw) for name,raw in images.items()}))
    for path in (R.SERVER_MAINTENANCE_TOOLS,R.SERVER_MAINTENANCE_TOOLS/digest):
        path.mkdir(mode=0o755,exist_ok=True)
        fd=OBS.open_path(path,directory=True)
        try:
            info=os.fstat(fd)
            if info.st_uid!=0 or info.st_mode&0o022:raise ValueError('unsafe maintenance executable cache')
        finally:os.close(fd)
    cache=R.SERVER_MAINTENANCE_TOOLS/digest
    for name,raw in images.items():frozen_file(cache/name,raw,0o444)
    launcher="import importlib.util,sys\nfrom pathlib import Path\np=Path(__file__).parent\ns=importlib.util.spec_from_file_location('maintenance_runner',p/'runner.py');r=importlib.util.module_from_spec(s);s.loader.exec_module(r)\n"
    for name in ('ROOT','SERVER_REGISTRY','SERVER_MAINTENANCE_ROOT','SERVER_MAINTENANCE_TOOLS'):
        launcher+='r.'+name+'=Path('+repr(str(getattr(R,name)))+')\n'
    launcher+="r.SERVER_MAINTENANCE_HELPER=p/'autonomy-server-maintenance.py'\nr.SERVER_OPERATIONS_HELPER=p/'autonomy-server-operations.py'\nsys.exit(r.main())\n"
    frozen_file(cache/'entry.py',launcher.encode(),0o444)
    atomic(manifest,dict(digest=digest))
    return cache


def stage_request(operation, phase, job, generation, create=False):
    stage=stage_path(operation,phase,job,generation)
    request_phase='apply' if phase=='reconcile' else phase
    path=stage/'request.json'
    if path.exists():raw=root_bytes(path,16384)[0]
    elif create:
        prior_apply=stage_path(operation,'apply',job,generation)
        if phase=='reconcile' and (prior_apply/'request.json').exists():
            raw=root_bytes(prior_apply/'request.json',16384)[0]
        else:
            raw=controller_bytes(R.job_path(job)/'server-maintenance'/(operation+'.'+request_phase+'.json'))
    else:raise FileNotFoundError('maintenance request not admitted')
    request=json.loads(raw,object_pairs_hook=OBS.unique)
    required={'schema_version','operation_id','pin_sha256','owner_job','owner_task','target_id','service_id','action','registry_sha256','expected_state_sha256','limits','generation'}
    if not isinstance(request,dict) or not required<=set(request) or set(request)-required-{'authority_sha256'} or type(request['schema_version']) is not int or request['schema_version']!=1:
        raise ValueError('unexpected maintenance request fields')
    owner_identity(request)
    if request.get('operation_id')!=operation or request.get('owner_job')!=job or request.get('generation')!=generation or request.get('action')!=ACTION or not key(request.get('pin_sha256')):
        raise ValueError('maintenance phase ownership differs')
    if not key(request.get('registry_sha256')) or not key(request.get('expected_state_sha256')):
        raise ValueError('maintenance observation binding differs')
    limits_text(request['limits'])
    if not path.exists():frozen_file(path,raw)
    authority=None
    if request_phase=='apply':
        authority_path=stage/'authority.json'
        if authority_path.exists():authority_raw=root_bytes(authority_path,16384)[0]
        elif create:
            prior_apply=stage_path(operation,'apply',job,generation)
            if phase=='reconcile' and (prior_apply/'authority.json').exists():
                authority_raw=root_bytes(prior_apply/'authority.json',16384)[0]
            else:
                authority_raw=controller_bytes(R.job_path(job)/'server-maintenance'/(operation+'.authority.json'))
            frozen_file(authority_path,authority_raw)
        else:raise FileNotFoundError('maintenance authority missing')
        authority=json.loads(authority_raw,object_pairs_hook=OBS.unique)
        validate(request,authority,request['registry_sha256'])
    return stage,request,raw,authority


def phase_unit(operation,phase,job,generation):
    return 'lectern-maintenance-'+operation+'-'+phase+'-'+job+'-'+str(generation)+'.service'


def phase_receipt(stage,request,raw,state,**extra):
    value=dict(schema_version=1,operation_id=request['operation_id'],owner_job=request['owner_job'],owner_task=request['owner_task'],
               pin_sha256=request['pin_sha256'],generation=request['generation'],request_sha256=sha(raw),phase=stage.parents[1].name,state=state,**extra)
    value['receipt_sha256']=sha(canonical(value));atomic(stage/'receipt.json',value)
    return value


def bound_pending(request,raw,phase,state,**extra):
    value=dict(schema_version=1,operation_id=request['operation_id'],owner_job=request['owner_job'],owner_task=request['owner_task'],
               pin_sha256=request['pin_sha256'],generation=request['generation'],request_sha256=sha(raw),phase=phase,state=state,**extra)
    value['receipt_sha256']=sha(canonical(value))
    return value


def phase_status(operation,phase,job,generation):
    stage,request,raw,_=stage_request(operation,phase,job,generation)
    if (stage/'receipt.json').exists():
        value=json.loads(root_bytes(stage/'receipt.json')[0]);verify_sealed(value)
        if value['state'] not in ('running','waiting'):return value
    if R.completion_service_active(phase_unit(operation,phase,job,generation)):
        return bound_pending(request,raw,phase,'running')
    # Only observation-only phases may become cancelled here. An inactive apply
    # can still own installed changes and must retain explicit reconciliation.
    if phase in ('validate','backup') and Executor(transaction_root(),None)._cancelled(request):
        return phase_receipt(stage,request,raw,'cancelled',reason='revoked generation is inactive')
    return bound_pending(request,raw,phase,'waiting',reason='explicit start or reconciliation required')


def phase_start(operation,phase,job,generation):
    root=operation_root(operation)
    with (root/'launch.lock').open('a') as guard:
        try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:
            _,request,raw,_=stage_request(operation,phase,job,generation)
            return bound_pending(request,raw,phase,'waiting',reason='launch or stop in progress')
        stage,request,raw,authority=stage_request(operation,phase,job,generation,True)
        prior=phase_status(operation,phase,job,generation)
        if prior['state'] not in ('waiting',):return prior
        executor=Executor(transaction_root(),None)
        cancelled=executor._cancelled(dict(operation_id=operation,generation=generation))
        if cancelled and phase!='reconcile':return phase_receipt(stage,request,raw,'cancelled',reason='generation revoked before launch')
        if phase=='reconcile':
            journal=transaction_root()/(operation+'.json')
            if not journal.exists() or json.loads(root_bytes(journal)[0]).get('phase') not in ('apply_intent','applied_pending_health','rollback_intent','applied'):
                return phase_receipt(stage,request,raw,'unavailable',reason='no owned mutation to reconcile')
        registry,registry_sha=OBS.load_registry(R.SERVER_REGISTRY)
        if registry_sha!=request['registry_sha256'] and phase!='reconcile':
            return phase_receipt(stage,request,raw,'conflict',reason='registry changed before launch')
        registry_path=root/'registry.json'
        if not registry_path.exists():frozen_file(registry_path,root_bytes(R.SERVER_REGISTRY,OBS.MAX_REGISTRY)[0])
        if sha(root_bytes(registry_path,OBS.MAX_REGISTRY)[0])!=request['registry_sha256']:
            raise ValueError('operation registry changed')
        cache=freeze_executables(operation)
        R.completion_launch_capacity()
        phase_receipt(stage,request,raw,'running',launched_at=time.time())
        seconds={'validate':60,'backup':660,'apply':180,'reconcile':180}[phase]
        command=['/usr/bin/systemd-run','--quiet','--collect','--unit='+phase_unit(operation,phase,job,generation),
                 '--property=RuntimeMaxSec='+str(seconds),'--property=MemoryMax=512M','--property=MemorySwapMax=0',
                 '--property=CPUQuota=100%','--property=TasksMax=64','--property=KillMode=control-group','--property=TimeoutStopSec=5s',
                 '--property=UMask=0077','--property=LimitFSIZE=67108864',
                 '/usr/bin/python3','-I','-S',str(cache/'entry.py'),'_server-maintenance','--job',job,
                 '--operation-id',operation,'--phase',phase,'--generation',str(generation)]
        R.run(command,pass_fds=(guard.fileno(),))
        return bound_pending(request,raw,phase,'running')


def phase_execute(operation,phase,job,generation):
    stage,request,raw,authority=stage_request(operation,phase,job,generation)
    current=Path('/proc/self/cgroup').read_text().strip().split('::')[-1]
    if not current.endswith('/'+phase_unit(operation,phase,job,generation)):
        raise ValueError('maintenance outside owned bounded unit')
    registry,registry_sha=OBS.load_registry(operation_root(operation)/'registry.json')
    backend=SystemdBackend(registry,registry_sha)
    backend.validation_guard=operation_root(operation)/'launch.lock'
    executor=Executor(transaction_root(),backend)
    if phase!='reconcile' and executor._cancelled(dict(operation_id=operation,generation=generation)):
        phase_receipt(stage,request,raw,'cancelled',reason='revoked before phase execution');return 0
    try:
        if phase!='reconcile' and OBS.load_registry(R.SERVER_REGISTRY)[1]!=registry_sha:
            raise Unavailable('live registry changed before execution')
        if phase=='validate':value=executor.validate_candidate(request)
        elif phase=='backup':value=executor.prepare_backup(request)
        else:value=executor.execute(request,authority)
        phase_receipt(stage,request,raw,value['state'],result=value)
        return 0
    except Exception as error:
        journal=transaction_root()/(operation+'.json')
        pending=journal.exists() and json.loads(root_bytes(journal)[0]).get('phase') in ('apply_intent','applied_pending_health','rollback_intent')
        phase_receipt(stage,request,raw,'reconciliation_required' if pending else 'unavailable',reason=type(error).__name__)
        return 1


def phase_stop(operation,generation):
    root=operation_root(operation)
    executor=Executor(transaction_root(),None)
    executor.cancel(operation,generation)  # tombstone precedes launch lock
    with (root/'launch.lock').open('a') as guard:
        try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:return dict(state='stopping',operation_id=operation,generation=generation)
        pending=False
        phases=root/'phases'
        if phases.exists():
            for path in sorted(phases.glob('*/*/*/request.json')):
                request=json.loads(root_bytes(path,16384)[0]);phase=path.parents[2].name
                old_generation=int(path.parent.name)
                if old_generation>generation:continue
                unit=phase_unit(operation,phase,request['owner_job'],old_generation)
                if not R.completion_service_active(unit):continue
                if phase in ('validate','backup'):
                    R.run(['/usr/bin/systemctl','stop','--no-block',unit],timeout=3,pass_fds=(guard.fileno(),))
                # Never kill the owned apply/rollback transaction halfway.
                pending=True
        journal=transaction_root()/(operation+'.json')
        if journal.exists():
            state=json.loads(root_bytes(journal)[0]).get('phase')
            # Even terminal journals can represent a failed applied candidate.
            # Only its actual receipt may resolve ownership; never renew its
            # mutation allowance from a generic process-stop acknowledgement.
            return dict(state='stopping' if pending else 'reconciliation_required',operation_id=operation,generation=generation,journal_state=state,no_effects=False)
        return dict(state='stopping' if pending else 'stopped',operation_id=operation,generation=generation,no_effects=not pending)


def dispatch(runtime,command,job,operation,phase,generation):
    global R
    R=SimpleNamespace(**runtime)
    if command=='server-maintenance-stop':return phase_stop(operation,generation)
    if command=='server-maintenance-status':return phase_status(operation,phase,job,generation)
    if command=='_server-maintenance':return phase_execute(operation,phase,job,generation)
    selected={'server-maintenance-validate':'validate','server-maintenance-backup':'backup','server-maintenance-apply':'apply','server-maintenance-reconcile':'reconcile'}.get(command)
    if selected is None:raise ValueError('unsupported maintenance command')
    return phase_start(operation,selected,job,generation)


def candidate_workload(memory, cpu, tasks):
    """Disposable, credential-free workload. Runs only inside a private unit."""
    import http.server
    import threading
    limits = dict(memory_max_bytes=memory, cpu_quota_percent=cpu, tasks_max=tasks)
    limits_text(limits)
    relative = next(line[3:] for line in Path('/proc/self/cgroup').read_text().splitlines() if line.startswith('0::'))
    cgroup = Path('/sys/fs/cgroup') / relative.lstrip('/')
    actual_memory = int((cgroup/'memory.max').read_text())
    actual_tasks = int((cgroup/'pids.max').read_text())
    quota, period = map(int, (cgroup/'cpu.max').read_text().split())
    if actual_memory != memory or actual_tasks != tasks or quota*100 != period*cpu:
        raise ValueError('actual cgroup limits differ')
    sample = {'sensor': 37.5, '_cpu_percent': 1.0, '_load': 0.25, '_cores': 1, '_sample': 1}
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path == '/metrics':
                raw = ('homelab_temp_celsius{sensor="synthetic"} ' + str(sample['sensor']) + '\n').encode()
            else:
                raw = canonical(sample)
            self.send_response(200)
            self.send_header('Content-Length', str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
        def log_message(self, *args):
            pass
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    allocation = bytearray(4*1024**2)
    allocation[0] = 1
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    url = 'http://127.0.0.1:' + str(server.server_port)
    with opener.open(url, timeout=2) as response:
        first = json.loads(response.read(8192))
    sample.update(sensor=38.5, _sample=2)
    with opener.open(url, timeout=2) as response:
        second = json.loads(response.read(8192))
    with opener.open(url+'/metrics', timeout=2) as response:
        metrics = response.read(8192).decode()
    server.shutdown()
    server.server_close()
    if first['_sample'] != 1 or second['_sample'] != 2 or '38.5' not in metrics:
        raise ValueError('synthetic sensor freshness or metrics failed')
    print(canonical(dict(profile='synthetic_sensor_v1', limits=limits, healthy=True, sample_changed=True,
                         http_json=True, metrics=True, uid=os.getuid(), cgroup_limits_verified=True)).decode())


if __name__ == '__main__':
    if len(sys.argv) == 5 and sys.argv[1] == '--candidate-workload':
        candidate_workload(*map(int, sys.argv[2:]))
    else:
        raise SystemExit('trusted runner dispatch required')
