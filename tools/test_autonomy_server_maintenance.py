#!/usr/bin/python3
"""No live service mutations: fixed transaction tested against a disposable adapter."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

SPEC = importlib.util.spec_from_file_location('maintenance', Path(__file__).with_name('autonomy-server-maintenance.py'))
M = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(M)


class Crash(BaseException):
    pass


class Backend:
    registry_sha = 'a' * 64

    def __init__(self):
        self.files = {'script': 'unchanged'}
        self.applied = 0
        self.activations = 0
        self.restores = 0
        self.backups = 0
        self.fail = None
        self.healthy = True
        self.external = False

    def resolve(self, target, service):
        if (target, service) != ('local', 'temp'):
            raise ValueError('unknown target')
        return {'identity': {'unit': 'disposable.service'}}

    def capture(self, resource):
        return {'files': copy.deepcopy(self.files)}

    def state_sha(self, before):
        return M.sha(M.canonical(before))

    def same_files(self, a, b):
        return a['files'] == b['files']

    def preflight(self, resource, limits):
        if self.fail == 'preflight':
            raise M.Unavailable('missing headroom')

    def backup(self, resource, journal, directory):
        self.backups += 1
        if self.fail == 'backup':
            return {'state': 'local_copy_only'}
        return {'state': 'offbox_restored_verified', 'before_sha256': journal['before_sha256'], 'snapshot_id': 'b' * 64}

    def validate_candidate(self, resource, limits, directory):
        if self.fail == 'validation_crash':
            raise Crash()
        return dict(executed=True, exit_code=0, output_sha256='8'*64, workload={'limits':limits})

    def validation_stopped(self,directory):
        return self.fail != 'validation_running'

    def install(self, resource, candidate, before):
        if self.capture(resource) != before:
            raise M.Unavailable('concurrent change')
        self.files['dropin'] = candidate.decode()
        self.applied += 1
        if self.fail == 'install_crash':
            raise Crash()

    def owns(self, resource, before, candidate):
        expected = dict(before['files'], dropin=candidate.decode())
        return self.files == expected

    def restore(self, resource, before, candidate):
        if not self.owns(resource, before, candidate):
            raise M.Unavailable('changed')
        self.files = copy.deepcopy(before['files'])
        self.restores += 1
        if self.fail == 'restore_crash':
            raise Crash()

    def restore_directory(self, resource, before):
        pass

    def activate(self, resource):
        self.activations += 1
        if self.fail == 'activate_crash':
            raise Crash()

    def health(self, resource, limits, cancelled):
        if self.external:
            self.files['dropin'] = 'foreign admin edit'
        if callable(self.fail):
            self.fail()
        return [{'healthy': self.healthy}]

    def restored_health(self, resource):
        return [{'healthy': self.fail != 'restore_unhealthy'}]

    def invocation(self, resource):
        return 'e' * 32


class MaintenanceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.backend = Backend()
        self.executor = M.Executor(self.temp.name, self.backend)
        self.request = dict(schema_version=1, operation_id='1' * 64, target_id='local', service_id='temp',
                            action=M.ACTION, registry_sha256=self.backend.registry_sha, pin_sha256='6'*64, owner_task=1, owner_job='11111111-1111-4111-8111-111111111111',
                            expected_state_sha256=self.backend.state_sha(self.backend.capture({})),
                            authority_sha256='0' * 64, generation=1,
                            limits=dict(memory_max_bytes=256 * 1024**2, cpu_quota_percent=50, tasks_max=64))

    def admit(self, prepare=True):
        validation_request = dict(self.request, owner_task=2, owner_job='22222222-2222-4222-8222-222222222222')
        validation = self.executor.validate_candidate(validation_request)
        backup = self.executor.prepare_backup(self.request) if prepare else {'receipt_sha256': '9' * 64}
        authority = {k: self.request[k] for k in ('operation_id', 'target_id', 'service_id', 'registry_sha256', 'expected_state_sha256', 'action', 'pin_sha256', 'owner_job', 'owner_task')}
        authority.update(request_sha256=M.request_digest(self.request), audit_receipt_sha256={'auditor_a': '2' * 64, 'auditor_b': '3' * 64}, candidate_review_sha256='4' * 64, backup_receipt_sha256=backup['receipt_sha256'],validation_receipt_sha256=validation['receipt_sha256'],validation_owner_job=validation['owner_job'],validation_owner_task=validation['owner_task'])
        self.request['authority_sha256'] = M.sha(M.canonical(authority))
        return authority

    def test_apply_is_idempotent_and_before_state_remains_restorable(self):
        authority = self.admit()
        receipt = self.executor.execute(self.request, authority)
        self.assertEqual(receipt['state'], 'applied')
        self.assertEqual(receipt['candidate_sha256'], M.sha(M.canonical(self.request['limits'])))
        self.assertEqual(self.executor.execute(self.request, authority), receipt)
        self.assertEqual((self.backend.applied, self.backend.backups), (1, 1))
        self.executor.cancel(self.request['operation_id'], 1)
        restored = self.executor.execute(self.request, authority)
        self.assertEqual(restored['state'], 'rolled_back')
        self.assertEqual(self.backend.files, {'script': 'unchanged'})
        journal = json.loads((Path(self.temp.name) / (self.request['operation_id'] + '.json')).read_bytes())
        self.assertEqual(journal['apply_receipt'], receipt)

    def test_local_backup_or_missing_proof_never_authorizes_mutation(self):
        authority = self.admit(False)
        self.assertEqual(self.executor.execute(self.request, authority)['state'], 'unavailable')
        self.assertEqual(self.backend.applied, 0)
        self.backend.fail = 'backup'
        with self.assertRaises(M.Unavailable):
            self.executor.prepare_backup(self.request)

    def test_pre_request_revocation_is_durable(self):
        authority = self.admit()
        self.executor.cancel(self.request['operation_id'], 1)
        restored_executor = M.Executor(self.temp.name, self.backend)
        self.assertEqual(restored_executor.execute(self.request, authority)['state'], 'cancelled')
        self.assertEqual(self.backend.applied, 0)

    def test_cancel_after_install_restores_without_model(self):
        authority = self.admit()
        self.backend.fail = lambda: self.executor.cancel(self.request['operation_id'], 1)
        self.assertEqual(self.executor.execute(self.request, authority)['state'], 'rolled_back')
        self.assertEqual(self.backend.files, {'script': 'unchanged'})

    def test_pre_effect_cancel_can_resume_same_authority_new_generation(self):
        authority = self.admit()
        old = dict(self.request)
        self.executor.cancel(self.request['operation_id'],1)
        self.assertEqual(self.executor.execute(self.request,authority)['state'],'cancelled')
        self.request['generation'] = 2
        self.assertEqual(self.executor.execute(self.request,authority)['state'],'applied')
        with self.assertRaises(M.Unavailable):
            self.executor.execute(old,authority)
        self.assertEqual(self.backend.applied,1)

    def test_only_explicit_preeffect_failure_can_renew(self):
        authority=self.admit()
        self.backend.fail='preflight'
        receipt=self.executor.execute(self.request,authority)
        self.assertEqual(receipt['state'],'unavailable')
        self.assertIs(receipt['no_effects'],True)
        self.assertIs(receipt['effects_started'],False)
        self.backend.fail=None
        self.request['generation']=2
        applied=self.executor.execute(self.request,authority)
        self.assertEqual(applied['state'],'applied')
        self.assertIs(applied['effects_started'],True)
        self.assertIs(applied['no_effects'],False)
        self.request['generation']=3
        with self.assertRaises(M.Unavailable):self.executor.execute(self.request,authority)

    def test_external_conflict_blocks_new_operation_until_bound_inspection(self):
        self.executor=M.Executor(Path(self.temp.name)/'transactions',self.backend)
        authority=self.admit();self.backend.external=True
        self.assertEqual(self.executor.execute(self.request,authority)['state'],'rollback_conflict')
        old=json.loads((self.executor.root/(self.request['operation_id']+'.json')).read_bytes())
        self.backend.external=False
        self.request['operation_id']=M.sha(b'new separately audited successor')
        self.request['expected_state_sha256']=self.backend.state_sha(self.backend.capture({}))
        successor_authority=self.admit()
        refused=self.executor.execute(self.request,successor_authority)
        self.assertEqual(refused['state'],'conflict')
        self.assertEqual(self.backend.applied,1)
        before=self.backend.capture({});current=self.backend.state_sha(before);inspection='9'*64
        proof=dict(state='external_healthy',before_sha256=old['before_sha256'],candidate_sha256=old['candidate_sha256'],operation_id=old['operation_id'],inspection_id=inspection,request_sha256=old['request_sha256'],authority_sha256=old['authority_sha256'],conflict_receipt_sha256=old['receipt']['receipt_sha256'],registry_sha256=self.request['registry_sha256'],profile='registered_service_external_health_v1',no_mutation=True,owned_candidate=False,current_state_sha256=current,post_state_sha256=current,invocation_id=self.backend.invocation({}),post_invocation_id=self.backend.invocation({}),observations=[dict(healthy=True,response_sha256='7'*64,metrics_sha256='8'*64)]*3)
        directory=self.executor.root.parent/'operations'/old['operation_id']/'inspections'/inspection;directory.mkdir(parents=True)
        def publish():
            proof.pop('receipt_sha256',None);proof['receipt_sha256']=M.sha(M.canonical(proof))
            outer=dict(state='external_healthy',phase='inspect',operation_id=old['operation_id'],inspection_id=inspection,result=proof);outer['receipt_sha256']=M.sha(M.canonical(outer));M.atomic(directory/'receipt.json',outer)
        with patch.object(M,'root_bytes',side_effect=lambda path,*args:(path.read_bytes(),0o600)):
            publish();self.assertTrue(self.executor._external_generation_inspected(old,before,self.request,{}))
            later={'files':dict(before['files'],script='legitimate later external version')}
            self.assertTrue(self.executor._external_generation_inspected(old,later,self.request,{}))
            proof['conflict_receipt_sha256']='0'*64;publish();self.assertFalse(self.executor._external_generation_inspected(old,before,self.request,{}))
            proof['conflict_receipt_sha256']=old['receipt']['receipt_sha256'];proof['observations'][0]['healthy']=False;publish();self.assertFalse(self.executor._external_generation_inspected(old,before,self.request,{}))
            proof['observations'][0]['healthy']=True;publish()
            self.request['generation']=2
            self.assertEqual(self.executor.execute(self.request,successor_authority)['state'],'applied')
        self.assertEqual(self.backend.applied,2)
        self.assertEqual(json.loads((self.executor.root/(old['operation_id']+'.json')).read_bytes()),old)

    def test_failed_candidate_cannot_reset_via_generation(self):
        authority = self.admit()
        self.backend.healthy = False
        self.assertEqual(self.executor.execute(self.request,authority)['state'],'rolled_back')
        self.request['generation'] = 2
        self.backend.healthy = True
        with self.assertRaises(M.Unavailable):
            self.executor.execute(self.request,authority)
        self.assertEqual(self.backend.applied,1)

    def test_failed_restore_health_retains_owned_reconciliation(self):
        authority = self.admit()
        self.backend.healthy = False
        self.backend.fail = 'restore_unhealthy'
        with self.assertRaises(M.Unavailable):
            self.executor.execute(self.request,authority)
        path = Path(self.temp.name)/(self.request['operation_id']+'.json')
        self.assertEqual(json.loads(path.read_bytes())['phase'],'rollback_intent')
        self.backend.fail = None
        self.assertEqual(self.executor.execute(self.request,authority)['state'],'rolled_back')
        self.assertEqual(self.backend.restores,1)

    def test_corrupted_validation_never_applies(self):
        authority = self.admit()
        path = Path(self.temp.name)/(self.request['operation_id']+'.validation')/'receipt.json'
        receipt = json.loads(path.read_bytes())
        receipt['evidence']['exit_code'] = 4
        path.write_bytes(M.canonical(receipt))
        self.assertEqual(self.executor.execute(self.request,authority)['state'],'unavailable')
        self.assertEqual(self.backend.applied,0)

    def test_interrupted_validation_retries_bounded_under_same_identity(self):
        self.backend.fail = 'validation_crash'
        with self.assertRaises(Crash):
            self.executor.validate_candidate(self.request)
        with self.assertRaises(M.Unavailable):
            self.executor.validate_candidate(self.request)
        path=Path(self.temp.name)/(self.request['operation_id']+'.validation')/'intent.json'
        value=json.loads(path.read_bytes());value['started_at']-=61
        path.write_bytes(M.canonical(value))
        self.backend.fail='validation_running'
        with self.assertRaises(M.Unavailable):
            self.executor.validate_candidate(self.request)
        self.backend.fail=None
        receipt=self.executor.validate_candidate(self.request)
        self.assertEqual(receipt['state'],'validated')
        self.assertEqual(len(receipt['attempts']),2)
        self.assertEqual(receipt['attempts'][0]['state'],'interrupted')

    def test_pending_and_terminal_phase_bind_exact_owner_request(self):
        raw=M.canonical(self.request)
        pending=M.bound_pending(self.request,raw,'validate','running')
        M.verify_sealed(pending)
        self.assertEqual(pending['owner_task'],self.request['owner_task'])
        self.assertEqual(pending['pin_sha256'],self.request['pin_sha256'])
        self.assertEqual(pending['request_sha256'],M.sha(raw))
        stage=Path(self.temp.name)/'phases'/'validate'/self.request['owner_job']/'1'
        stage.mkdir(parents=True)
        terminal=M.phase_receipt(stage,self.request,raw,'validated',result={'executed':True})
        self.assertEqual(terminal['phase'],'validate')
        self.assertEqual(terminal['request_sha256'],pending['request_sha256'])
        M.verify_sealed(terminal)

    def test_status_never_launches_an_absent_execution(self):
        stage=Path(self.temp.name)/'empty';stage.mkdir()
        raw=M.canonical(self.request)
        runtime=SimpleNamespace(completion_service_active=lambda name:False,run=lambda *args: self.fail('status launched command'))
        with patch.object(M,'stage_request',return_value=(stage,self.request,raw,None)),patch.object(M,'R',runtime),patch.object(M,'transaction_root',return_value=Path(self.temp.name)):
            status=M.phase_status(self.request['operation_id'],'validate',self.request['owner_job'],1)
        self.assertEqual(status['state'],'waiting')
        self.assertEqual(status['request_sha256'],M.sha(raw))

    def test_tombstoned_inactive_status_seals_cancel_only_before_effects(self):
        self.executor.cancel(self.request['operation_id'],1)
        raw=M.canonical(self.request)
        for phase in ('validate','backup','apply','reconcile'):
            stage=Path(self.temp.name)/'phases'/phase/self.request['owner_job']/'1'
            stage.mkdir(parents=True)
            runtime=SimpleNamespace(completion_service_active=lambda name:False,run=lambda *args:self.fail('status launched'))
            with patch.object(M,'stage_request',return_value=(stage,self.request,raw,None)),patch.object(M,'R',runtime),patch.object(M,'transaction_root',return_value=Path(self.temp.name)):
                status=M.phase_status(self.request['operation_id'],phase,self.request['owner_job'],1)
            self.assertEqual(status['state'],'cancelled' if phase in ('validate','backup') else 'waiting')
            M.verify_sealed(status)
            self.assertEqual((stage/'receipt.json').exists(),phase in ('validate','backup'))
        # A tombstone must not suppress a currently draining owned unit.
        with patch.object(M,'stage_request',return_value=(stage,self.request,raw,None)),patch.object(M,'R',SimpleNamespace(completion_service_active=lambda name:True)),patch.object(M,'transaction_root',return_value=Path(self.temp.name)):
            self.assertEqual(M.phase_status(self.request['operation_id'],'validate',self.request['owner_job'],1)['state'],'running')

    def test_stop_never_claims_no_effects_for_any_transaction_journal(self):
        root=Path(self.temp.name)/'operation';root.mkdir()
        runtime=SimpleNamespace(completion_service_active=lambda name:False,run=lambda *args,**kw:self.fail('unexpected command'))
        with patch.object(M,'operation_root',return_value=root),patch.object(M,'transaction_root',return_value=Path(self.temp.name)),patch.object(M,'R',runtime),patch.object(M,'root_bytes',side_effect=lambda path:(path.read_bytes(),0o600)):
            clean=M.phase_stop(self.request['operation_id'],1)
            self.assertEqual((clean['state'],clean['no_effects']),('stopped',True))
            for phase in ('reserved','apply_intent','applied','rolled_back','rollback_conflict','cancelled','unavailable'):
                M.atomic(Path(self.temp.name)/(self.request['operation_id']+'.json'),{'phase':phase})
                result=M.phase_stop(self.request['operation_id'],1)
                self.assertEqual(result['state'],'reconciliation_required')
                self.assertFalse(result['no_effects'])
                self.assertEqual(result['journal_state'],phase)

    def test_startup_readiness_grace_preserves_strict_health(self):
        backend=M.SystemdBackend({},'a'*64)
        with patch.object(backend,'_health_once',side_effect=[ConnectionRefusedError(),{'healthy':True}]) as probe,patch.object(M.time,'sleep'):
            self.assertTrue(backend._wait_ready({})['healthy'])
            self.assertEqual(probe.call_count,2)

    def test_crash_reconciles_owned_change_once(self):
        authority = self.admit()
        self.backend.fail = 'install_crash'
        with self.assertRaises(Crash):
            self.executor.execute(self.request, authority)
        self.backend.fail = None
        result = M.Executor(self.temp.name, self.backend).execute(self.request, authority)
        self.assertEqual(result['state'], 'rolled_back')
        self.assertEqual((self.backend.applied, self.backend.restores), (1, 1))

    def test_crash_after_restore_before_reload_reconciles(self):
        authority = self.admit()
        self.backend.healthy = False
        self.backend.fail = 'restore_crash'
        with self.assertRaises(Crash):
            self.executor.execute(self.request, authority)
        self.backend.fail = None
        self.assertEqual(self.executor.execute(self.request, authority)['state'], 'rolled_back')
        self.assertEqual(self.backend.restores, 1)

    def test_foreign_change_never_overwritten(self):
        authority = self.admit()
        self.backend.external = True
        self.backend.healthy = False
        self.assertEqual(self.executor.execute(self.request, authority)['state'], 'rollback_conflict')
        self.assertEqual(self.backend.files['dropin'], 'foreign admin edit')
        self.assertEqual(self.backend.restores, 0)

    def test_new_id_cannot_skip_interrupted_mutation(self):
        authority = self.admit()
        self.backend.fail = 'install_crash'
        with self.assertRaises(Crash):
            self.executor.execute(self.request, authority)
        self.backend.fail = None
        self.request['operation_id'] = '5' * 64
        self.request['expected_state_sha256'] = self.backend.state_sha(self.backend.capture({}))
        authority = self.admit()
        self.assertEqual(self.executor.execute(self.request, authority)['state'], 'conflict')
        self.assertEqual(self.backend.files, {'script': 'unchanged'})

    def test_changed_configuration_during_backup_conflicts(self):
        authority = self.admit()
        self.backend.files['script'] = 'new owner bytes'
        self.assertEqual(self.executor.execute(self.request, authority)['state'], 'conflict')
        self.assertEqual(self.backend.applied, 0)

    def test_no_unbounded_limits_or_arbitrary_directives(self):
        for change in ({'cpu_quota_percent': 0}, {'tasks_max': True}, {'memory_max_bytes': 100}, {'ExecStart': '/bin/sh'}):
            limits = dict(self.request['limits'], **change)
            with self.assertRaises(ValueError):
                M.limits_text(limits)
        authority = self.admit()
        self.request['path'] = '/etc/shadow'
        with self.assertRaises(ValueError):
            self.executor.execute(self.request, authority)


if __name__ == '__main__':
    unittest.main()
