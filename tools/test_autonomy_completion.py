"""Documentary completion lifecycle: real archives, frozen baseline and Go validator."""
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import uuid

REAL_CHOWN = os.chown

SPEC = importlib.util.spec_from_file_location('completion_runner', Path(__file__).with_name('autonomy-runner.py'))
RUNNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNNER)


class CompletionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.jobs = self.root / 'jobs'
        self.jobs.mkdir()
        self.states = {}
        for target, value in [("ROOT", self.jobs), ("ARTIFACT_LOCK", self.root / 'artifact.lock')]:
            patcher = patch.object(RUNNER, target, value)
            patcher.start(); self.addCleanup(patcher.stop)
        for target, value in [("status", lambda job: {'state': self.states.get(job, 'done')}),
                              ("ensure_work", lambda p: p / 'work'),
                              ("completion_capacity", lambda: None),
                              ("completion_launch_capacity", lambda: None),
                              ("completion_active", lambda job: False),
                              ("write_new", self.write_new)]:
            patcher = patch.object(RUNNER, target, value)
            patcher.start(); self.addCleanup(patcher.stop)
        patcher = patch.object(RUNNER.os, 'chown', lambda *args, **kwargs: None)
        patcher.start(); self.addCleanup(patcher.stop)
        self.source = self.new_job({'WORKSHOP.md': b'original claims\n', 'code.py': b'assert True\n', 'autonomy-report.json': b'{"old":true}\r\n'})
        self.reviewer = self.new_job({'review.md': b'Rejected for documentary provenance\n'})
        self.destination = self.new_job({})
        self.archive(self.source); self.archive(self.reviewer)
        self.cli = os.environ.get('LECTERN_OVERLAY_TEST_CLI')
        if self.cli:
            patcher = patch.object(RUNNER, 'OVERLAY_CLI', self.cli)
            patcher.start(); self.addCleanup(patcher.stop)
        else:
            # Archive/source lifecycle tests don't need an executable validator;
            # actual overlay/derived tests below explicitly require the real Go CLI.
            patcher = patch.object(RUNNER, 'completion_inspect', self.inspect_fixture)
            patcher.start(); self.addCleanup(patcher.stop)

    @staticmethod
    def write_new(path, data, mode=0o640):
        with path.open('x') as out:
            out.write(data)
        path.chmod(mode)

    @staticmethod
    def inspect_fixture(path):
        RUNNER.completion_paths(path)
        h = hashlib.sha256()
        for item in sorted(path.rglob('*')):
            h.update(str(item.relative_to(path)).encode())
            h.update(str(item.stat().st_mode & 0o777).encode())
            if item.is_file(): h.update(item.read_bytes())
        return h.hexdigest()

    def new_job(self, files):
        job = str(uuid.uuid4())
        work = self.jobs / job / 'work'
        work.mkdir(parents=True)
        for name, data in files.items():
            target = work / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        return job

    def archive(self, job):
        directory = self.jobs / job
        path = directory / 'artifact.tar.gz'
        with tarfile.open(path, 'w:gz') as out:
            out.add(directory / 'work', arcname='work')
        path.chmod(0o440)
        stage = directory / 'artifact-state'
        stage.mkdir(exist_ok=True)
        (stage / 'receipt.json').write_text(json.dumps({'state': 'ready', 'sha256': RUNNER.digest_file(path)}))
        return path

    def prepare(self):
        return RUNNER.completion_prepare(self.destination, self.source, self.reviewer)

    def assert_real_cli(self):
        if not self.cli:
            self.skipTest('set LECTERN_OVERLAY_TEST_CLI to built Lectern for real overlay acceptance')

    def test_prepare_uses_archived_source_and_retains_old_report(self):
        # A later mutable worktree change must not affect the inherited baseline.
        (self.jobs / self.source / 'work/code.py').write_bytes(b'changed after export')
        receipt = self.prepare()
        base = self.jobs / self.destination / 'completion/baseline'
        self.assertEqual((base / 'code.py').read_bytes(), b'assert True\n')
        self.assertFalse((base / 'autonomy-report.json').exists())
        self.assertEqual((base / '.lectern-reports' / self.source / 'autonomy-report.json').read_bytes(), b'{"old":true}\r\n')
        self.assertEqual((base / '.lectern-review' / self.reviewer / 'work/review.md').read_bytes(), b'Rejected for documentary provenance\n')
        self.assertEqual(receipt, self.prepare())
        self.assertEqual(receipt['source_archive_sha256'], RUNNER.digest_file(self.jobs / self.source / 'artifact.tar.gz'))
        with self.assertRaises(ValueError):
            RUNNER.completion_prepare(self.destination, self.reviewer, self.source)

    def test_source_archive_hash_mismatch_refused(self):
        artifact = self.jobs / self.source / 'artifact.tar.gz'
        artifact.chmod(0o640)
        with artifact.open('ab') as stream:
            stream.write(b'tamper')
        artifact.chmod(0o440)
        with self.assertRaisesRegex(RuntimeError, 'checksum mismatch'):
            self.prepare()

    def test_archive_path_and_link_attacks_refused(self):
        for name, link in [('work/../../escape', False), ('work/escape', True), ('/etc/passwd', False)]:
            with self.subTest(name=name):
                source = self.new_job({})
                archive = self.jobs / source / 'artifact.tar.gz'
                with tarfile.open(archive, 'w:gz') as out:
                    root = tarfile.TarInfo('work'); root.type = tarfile.DIRTYPE; root.mode = 0o755; out.addfile(root)
                    entry = tarfile.TarInfo(name); entry.mode = 0o644
                    if link:
                        entry.type = tarfile.SYMTYPE; entry.linkname = '/etc/passwd'
                    out.addfile(entry)
                archive.chmod(0o440)
                state = self.jobs / source / 'artifact-state'; state.mkdir()
                (state / 'receipt.json').write_text(json.dumps({'state': 'ready', 'sha256': RUNNER.digest_file(archive)}))
                with self.assertRaises(ValueError):
                    RUNNER.completion_extract_archive(source, self.root / str(uuid.uuid4()))

    def test_frozen_baseline_tampering_refused(self):
        self.prepare()
        stage = self.jobs / self.destination / 'completion'
        (stage / 'baseline/code.py').write_text('changed')
        with self.assertRaisesRegex(RuntimeError, 'baseline identity changed'):
            RUNNER.completion_binding(stage)

    def test_resume_preserves_baseline_without_new_in_tree_handoff(self):
        original = self.prepare()
        work = self.jobs / self.destination / 'work'
        (work / 'autonomy-report.json').write_bytes(b'{"partial":true}\r\n')
        self.states[self.destination] = 'failed'
        self.archive(self.destination)
        next_job = self.new_job({})
        receipt = RUNNER.completion_resume(next_job, self.destination)
        self.assertEqual(receipt['baseline_sha256'], original['baseline_sha256'])
        new_stage = self.jobs / next_job / 'completion'
        self.assertEqual((new_stage / 'resume-submission.json').read_bytes(), b'{"partial":true}\r\n')
        self.assertFalse((self.jobs / next_job / 'work/.lectern-reports' / self.destination).exists())
        self.assertEqual((self.jobs / next_job / 'work/autonomy-report.json').read_bytes(), b'{"partial":true}\r\n')
        self.assertFalse((new_stage / 'receipt.json').exists())
        self.assertEqual(RUNNER.completion_json(new_stage / 'prepare-receipt.json')['state'], 'ready')
        self.assertEqual(RUNNER.completion_inspect(self.jobs / next_job / 'completion/baseline'), original['baseline_sha256'])

    def test_completion_resume_refuses_running_source(self):
        self.prepare()
        for state in ['running']:
            self.states[self.destination] = state
            with self.assertRaises(ValueError):
                RUNNER.completion_resume(self.new_job({}), self.destination)

    def test_async_launch_is_bounded_and_rejected_is_terminal(self):
        self.prepare()
        with patch.object(RUNNER, 'run') as launch:
            self.assertEqual(RUNNER.completion_reconstruct(self.destination)['state'], 'running')
            args = launch.call_args.args[0]
            self.assertIn('--property=RuntimeMaxSec=600', args)
            self.assertIn('--property=MemoryMax=2G', args)
            self.assertIn('--property=CPUQuota=200%', args)
        stage = self.jobs / self.destination / 'completion'
        RUNNER.completion_write(stage / 'receipt.json', {'state': 'rejected', 'reason': 'production changed'})
        with patch.object(RUNNER, 'run') as launch:
            self.assertEqual(RUNNER.completion_reconstruct(self.destination)['state'], 'rejected')
            launch.assert_not_called()

    def test_validator_operational_failure_retries(self):
        self.prepare()
        self.archive(self.destination)
        with patch.object(RUNNER.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'temporarily unavailable')):
            self.assertEqual(RUNNER.completion_execute(self.destination), 1)
        receipt = RUNNER.completion_json(self.jobs / self.destination / 'completion/receipt.json')
        self.assertEqual(receipt['state'], 'waiting')
        self.assertIn('retry_at', receipt)

    def test_prepare_crash_reuses_exact_reserved_destination(self):
        original_extract = RUNNER.completion_extract_archive
        def interrupted(source, destination):
            if source == self.reviewer:
                raise OSError('simulated storage interruption')
            return original_extract(source, destination)
        with patch.object(RUNNER, 'completion_extract_archive', interrupted):
            with self.assertRaises(OSError):
                self.prepare()
        stage = self.jobs / self.destination / 'completion'
        self.assertTrue((stage / 'preparation.json').exists())
        self.assertFalse((stage / 'baseline.json').exists())
        receipt = self.prepare()
        self.assertEqual(receipt['state'], 'ready')
        self.assertEqual(receipt, self.prepare())

    def test_async_prepare_binds_request_and_does_not_copy_under_poll(self):
        with patch.object(RUNNER, 'completion_service_active', return_value=False), patch.object(RUNNER, 'run') as launch:
            receipt = RUNNER.completion_prepare_status(self.destination, self.source, self.reviewer)
            self.assertEqual(receipt['state'], 'preparing')
            self.assertIn('_completion-prepare', launch.call_args.args[0])
            self.assertFalse(any((self.jobs / self.destination / 'work').iterdir()))
        self.assertEqual(RUNNER.completion_prepare_execute(self.destination, self.source, self.reviewer), 0)
        with patch.object(RUNNER, 'completion_service_active', return_value=False), patch.object(RUNNER, 'completion_inspect', side_effect=AssertionError('poll must not scan full trees')):
            receipt = RUNNER.completion_prepare_status(self.destination, self.source, self.reviewer)
            self.assertEqual(receipt['state'], 'ready')

    def test_done_report_correction_resume_retains_baseline(self):
        original = self.prepare()
        (self.jobs / self.destination / 'work/autonomy-report.json').write_text('malformed report')
        self.states[self.destination] = 'done'
        self.archive(self.destination)
        next_job = self.new_job({})
        receipt = RUNNER.completion_resume(next_job, self.destination)
        self.assertEqual(receipt['baseline_sha256'], original['baseline_sha256'])
        stage = self.jobs / self.destination / 'completion'
        RUNNER.completion_write(stage / 'receipt.json', {'state': 'rejected'})
        with self.assertRaises(ValueError):
            RUNNER.completion_resume(self.new_job({}), self.destination)

    def test_baseline_corruption_is_operational_not_overlay_rejection(self):
        self.prepare()
        (self.jobs / self.destination / 'completion/baseline/code.py').write_text('corrupted trusted baseline')
        self.assertEqual(RUNNER.completion_execute(self.destination), 1)
        receipt = RUNNER.completion_json(self.jobs / self.destination / 'completion/receipt.json')
        self.assertEqual(receipt['state'], 'waiting')

    def test_archive_receipt_corruption_is_operational(self):
        self.prepare()
        self.archive(self.destination)
        (self.jobs / self.destination / 'artifact-state/receipt.json').write_text('{broken')
        self.assertEqual(RUNNER.completion_execute(self.destination), 1)
        receipt = RUNNER.completion_json(self.jobs / self.destination / 'completion/receipt.json')
        self.assertEqual(receipt['state'], 'waiting')

    def test_stop_cancels_only_fixed_completion_units_and_keeps_receipts(self):
        self.prepare()
        stage = self.jobs / self.destination / 'completion'
        RUNNER.completion_write(stage / 'receipt.json', {'state': 'running'})
        baseline = (stage / 'baseline.json').read_bytes()
        with patch.object(RUNNER, 'completion_service_active', side_effect=[True, False, True, False]), patch.object(RUNNER, 'run') as command:
            self.assertEqual(RUNNER.completion_stop(self.destination), {'state': 'stopped'})
            self.assertEqual([call.args[0] for call in command.call_args_list], [
                ['/usr/bin/systemctl', 'stop', RUNNER.completion_prepare_unit(self.destination)],
                ['/usr/bin/systemctl', 'stop', RUNNER.completion_unit(self.destination)]])
        self.assertEqual((stage / 'baseline.json').read_bytes(), baseline)
        self.assertEqual(RUNNER.completion_json(stage / 'receipt.json')['state'], 'running')

    def test_auditor_receives_exact_archive_evidence(self):
        (self.jobs / self.source / 'work/code.py').write_text('later mutable code')
        receipt = RUNNER.copy_archive_review(self.destination, self.source)
        copied = self.jobs / self.destination / 'work/.lectern-review' / self.source
        self.assertEqual((copied / 'work/code.py').read_text(), 'assert True\n')
        manifest = json.loads((copied / 'manifest.json').read_text())
        self.assertEqual(manifest['source_archive_sha256'], receipt['source_archive_sha256'])
        self.assertEqual(receipt['source_archive_sha256'], RUNNER.digest_file(self.jobs / self.source / 'artifact.tar.gz'))

    def test_general_planner_evidence_preserves_links_without_host_reads(self):
        work=self.jobs/self.source/'work'
        secret=self.root/'host-secret';secret.write_bytes(b'NEVER READ HOST TARGET')
        external=self.root/'host-directory';external.mkdir();(external/'hidden').write_bytes(b'NEVER TRAVERSE HOST DIRECTORY')
        (work/'local-alias').symlink_to('code.py')
        (work/'external-alias').symlink_to(secret)
        (work/'directory-alias').symlink_to(external,target_is_directory=True)
        (work/'broken').symlink_to('../../missing')
        os.link(work/'code.py',work/'hardlinked-code.py')
        (self.jobs/self.source/'artifact.tar.gz').chmod(0o600)
        self.archive(self.source)
        source_digest=RUNNER.digest_file(self.jobs/self.source/'artifact.tar.gz')
        original_digest=RUNNER.digest_file
        def guarded_digest(path):
            self.assertNotEqual(Path(path),secret)
            self.assertNotEqual(Path(path),external/'hidden')
            self.assertFalse(Path(path).is_symlink(),'link text must never be opened for hashing')
            return original_digest(path)
        def guarded_chown(path,*args,**kwargs):
            if Path(path).is_symlink():self.assertFalse(kwargs.get('follow_symlinks',True))
        with patch.object(RUNNER,'digest_file',side_effect=guarded_digest),patch.object(RUNNER.os,'chown',side_effect=guarded_chown):
            receipt=RUNNER.copy_archive_review(self.destination,self.source)
            self.assertEqual(receipt,RUNNER.copy_archive_review(self.destination,self.source))
        evidence=self.jobs/self.destination/'work/.lectern-review'/self.source
        copied=evidence/'work'
        self.assertEqual(receipt['evidence_digest_scheme'],'general-evidence-v1')
        self.assertEqual(receipt['source_archive_sha256'],source_digest)
        self.assertEqual(os.readlink(copied/'external-alias'),str(secret))
        self.assertEqual(os.readlink(copied/'broken'),'../../missing')
        self.assertEqual((copied/'hardlinked-code.py').read_bytes(),b'assert True\n')
        self.assertEqual((copied/'hardlinked-code.py').stat().st_nlink,1)
        self.assertNotEqual((copied/'hardlinked-code.py').stat().st_ino,(copied/'code.py').stat().st_ino)
        manifest=json.loads((evidence/'manifest.json').read_text())
        self.assertNotIn('NEVER READ',json.dumps(manifest));self.assertNotIn('directory-alias/hidden',[r['path'] for r in manifest['files']])
        self.assertIn({'path':'external-alias','mode':0o777,'sha256':None,'link':str(secret),'kind':'symlink'},manifest['files'])
        with self.assertRaises(ValueError):RUNNER.completion_extract_archive(self.source,self.root/'strict-doc')
        (copied/'local-alias').unlink();(copied/'local-alias').symlink_to('different.py')
        with self.assertRaisesRegex(RuntimeError,'evidence changed'):RUNNER.copy_archive_review(self.destination,self.source)

    def test_general_evidence_rejects_link_parent_escape_cycle_and_expansion(self):
        def member(name,kind=tarfile.REGTYPE,target='',data=b'x'):
            value=tarfile.TarInfo(name);value.type=kind;value.mode=0o755 if kind==tarfile.DIRTYPE else 0o644
            value.linkname=target;value.size=len(data) if kind==tarfile.REGTYPE else 0
            return value,data
        root=member('work',tarfile.DIRTYPE)
        cases=[
            [root,member('work/escape',tarfile.SYMTYPE,str(self.root)),member('work/escape/host-written')],
            [root,member('work/hard',tarfile.LNKTYPE,'../outside')],
            [root,member('work/a',tarfile.LNKTYPE,'work/b'),member('work/b',tarfile.LNKTYPE,'work/a')],
            [root,member('work/link',tarfile.SYMTYPE,'target'),member('work/hard',tarfile.LNKTYPE,'work/link')],
            [root,member('work/file',data=b'1234'),member('work/hard',tarfile.LNKTYPE,'work/file')],
        ]
        for index,items in enumerate(cases):
            with self.subTest(index=index):
                archive=self.jobs/self.source/'artifact.tar.gz';archive.chmod(0o600)
                with tarfile.open(archive,'w:gz') as out:
                    for entry,data in items:out.addfile(entry,io.BytesIO(data) if entry.isfile() else None)
                archive.chmod(0o440)
                (self.jobs/self.source/'artifact-state/receipt.json').write_text(json.dumps({'state':'ready','sha256':RUNNER.digest_file(archive)}))
                with patch.object(RUNNER,'COMPLETION_MAX_BYTES',7 if index==4 else 1900*1024**2):
                    with self.assertRaises(ValueError):RUNNER.evidence_extract_archive(self.source,self.root/('unsafe-'+str(index)))
        self.assertFalse((self.root/'host-written').exists())

    def test_plain_evidence_keeps_legacy_digest_and_receipt_verification(self):
        receipt=RUNNER.copy_archive_review(self.destination,self.source)
        copied=self.jobs/self.destination/'work/.lectern-review'/self.source/'work'
        self.assertEqual(receipt['evidence_tree_sha256'],RUNNER.completion_inspect(copied))
        ready=self.jobs/self.destination/'completion'/('archive-review-'+self.source+'-ready.json')
        legacy=dict(receipt);legacy.pop('evidence_digest_scheme');RUNNER.completion_write(ready,legacy)
        self.assertEqual(RUNNER.copy_archive_review(self.destination,self.source),legacy)

    def test_prepare_ownership_failure_cannot_publish_readiness(self):
        source_work = self.jobs / self.source / 'work'
        source_work.chmod(0o700)
        (self.jobs / self.source / 'artifact.tar.gz').chmod(0o640)
        self.archive(self.source)
        stage = RUNNER.completion_stage(self.destination, create=True)
        with patch.object(RUNNER.os, 'chown', side_effect=PermissionError('injected ownership failure')):
            self.assertEqual(RUNNER.completion_prepare_execute(self.destination, self.source, self.reviewer), 1)
        baseline_before = (stage / 'baseline.json').read_bytes()
        self.assertEqual(RUNNER.completion_json(stage / 'prepare-receipt.json')['state'], 'waiting')
        with patch.object(RUNNER, 'completion_service_active', return_value=False):
            polled = RUNNER.completion_prepare_status(self.destination, self.source, self.reviewer)
        self.assertNotEqual(polled['state'], 'ready')
        with patch.object(RUNNER.os, 'chown') as chown, patch.object(RUNNER, 'completion_extract_archive', side_effect=AssertionError('baseline must not be rebuilt')):
            self.assertEqual(RUNNER.completion_prepare_execute(self.destination, self.source, self.reviewer), 0)
            work = self.jobs / self.destination / 'work'
            admin = RUNNER.pwd.getpwnam('admin')
            chown.assert_any_call(work, admin.pw_uid, admin.pw_gid)
            chown.assert_any_call(work / 'code.py', admin.pw_uid, admin.pw_gid)
        self.assertEqual((stage / 'baseline.json').read_bytes(), baseline_before)
        self.assertEqual((self.jobs / self.destination / 'work').stat().st_mode & 0o777, 0o700)
        with patch.object(RUNNER, 'completion_service_active', return_value=False):
            self.assertEqual(RUNNER.completion_prepare_status(self.destination, self.source, self.reviewer)['state'], 'ready')

    def test_archive_identity_is_lightweight_but_rejects_bad_receipts(self):
        with patch.object(RUNNER, 'digest_file', side_effect=AssertionError('identity poll must not hash archive')):
            identity = RUNNER.archive_identity(self.source)
        self.assertEqual(identity['state'], 'ready')
        self.assertEqual(identity['sha256'], RUNNER.digest_file(self.jobs / self.source / 'artifact.tar.gz'))
        (self.jobs / self.source / 'artifact-state/receipt.json').write_text('{"state":"ready","sha256":"bad"}')
        with self.assertRaises(RuntimeError):
            RUNNER.archive_identity(self.source)

    def test_async_archive_copy_polls_lightweight_and_supports_multiple_sources(self):
        for source in [self.source, self.reviewer]:
            with patch.object(RUNNER, 'completion_service_active', return_value=False), patch.object(RUNNER, 'run') as launch, patch.object(RUNNER, 'completion_capacity', side_effect=AssertionError('poll must not scan retained storage')):
                self.assertEqual(RUNNER.completion_copy_status(self.destination, source, 'archive')['state'], 'copying')
                self.assertIn('_copy-archive-review', launch.call_args.args[0])
            self.assertEqual(RUNNER.completion_copy_execute(self.destination, source, 'archive'), 0)
            with patch.object(RUNNER, 'completion_service_active', return_value=False), patch.object(RUNNER, 'completion_inspect', side_effect=AssertionError('poll must not scan tree')), patch.object(RUNNER, 'run') as launch:
                receipt = RUNNER.completion_copy_status(self.destination, source, 'archive')
                self.assertEqual(receipt['state'], 'copied')
                launch.assert_not_called()
        copied = self.jobs / self.destination / 'work/.lectern-review'
        self.assertTrue((copied / self.source / 'work/code.py').exists())
        self.assertTrue((copied / self.reviewer / 'work/review.md').exists())

    def test_archive_copy_crash_reuses_only_matching_partial_namespace(self):
        original_extract = RUNNER.evidence_extract_archive
        def interrupt(source, destination):
            result = original_extract(source, destination)
            raise OSError('interrupted after archive extraction')
        with patch.object(RUNNER, 'evidence_extract_archive', interrupt):
            with self.assertRaises(OSError):
                RUNNER.copy_archive_review(self.destination, self.source)
        receipt = RUNNER.copy_archive_review(self.destination, self.source)
        self.assertEqual(receipt['state'], 'copied')
        self.assertEqual(receipt, RUNNER.copy_archive_review(self.destination, self.source))

    def test_copy_stop_includes_only_registered_destination_copy_units(self):
        stage = RUNNER.completion_stage(self.destination, create=True)
        for kind, source in [('derived', self.source), ('archive', self.reviewer)]:
            RUNNER.completion_write(RUNNER.completion_copy_receipt(stage, source, kind), {'state': 'copying', 'copy_source_job': source})
        with patch.object(RUNNER, 'completion_service_active', side_effect=[True, False]*4), patch.object(RUNNER, 'run') as stop:
            RUNNER.completion_stop(self.destination)
        names = {call.args[0][-1] for call in stop.call_args_list}
        self.assertEqual(names, {RUNNER.completion_prepare_unit(self.destination), RUNNER.completion_unit(self.destination), RUNNER.completion_copy_unit(self.destination, self.source, 'derived'), RUNNER.completion_copy_unit(self.destination, self.reviewer, 'archive')})
        self.assertTrue(RUNNER.completion_copy_receipt(stage, self.source, 'derived').exists())

    def test_async_resume_interruption_reuses_destination_and_rejects_corrupt_source(self):
        self.prepare()
        (self.jobs / self.destination / 'work/autonomy-report.json').write_text('{broken report')
        self.archive(self.destination)
        self.states[self.destination] = 'failed'
        next_job = self.new_job({})
        with patch.object(RUNNER, 'completion_service_active', return_value=False), patch.object(RUNNER, 'run') as launch:
            self.assertEqual(RUNNER.completion_copy_status(next_job, self.destination, 'resume')['state'], 'copying')
            self.assertIn('_completion-resume', launch.call_args.args[0])
        with patch.object(RUNNER, 'completion_copy', side_effect=OSError('interrupted baseline copy')):
            self.assertEqual(RUNNER.completion_copy_execute(next_job, self.destination, 'resume'), 1)
        stage = self.jobs / next_job / 'completion'
        self.assertTrue((stage / 'resume-intent.json').exists())
        self.assertFalse((stage / 'resume-ready.json').exists())
        self.assertEqual(RUNNER.completion_copy_execute(next_job, self.destination, 'resume'), 0)
        self.assertEqual((self.jobs / next_job / 'work/autonomy-report.json').read_text(), '{broken report')
        with patch.object(RUNNER, 'completion_service_active', return_value=False), patch.object(RUNNER, 'completion_inspect', side_effect=AssertionError('resume poll must be lightweight')):
            self.assertEqual(RUNNER.completion_copy_status(next_job, self.destination, 'resume')['state'], 'copied')
        corrupted = self.new_job({})
        RUNNER.completion_stage(corrupted, create=True)
        archive = self.jobs / self.destination / 'artifact.tar.gz'
        data = bytearray(archive.read_bytes()); data[-1] ^= 1
        archive.chmod(0o640); archive.write_bytes(data); archive.chmod(0o440)
        self.assertEqual(RUNNER.completion_copy_execute(corrupted, self.destination, 'resume'), 1)
        self.assertEqual(RUNNER.completion_json(self.jobs / corrupted / 'completion/copy-resume.json')['state'], 'waiting')
        self.assertFalse((self.jobs / corrupted / 'completion/resume-ready.json').exists())

    def test_resume_stop_includes_registered_resume_unit(self):
        stage = RUNNER.completion_stage(self.destination, create=True)
        RUNNER.completion_write(stage / 'copy-resume.json', {'state': 'copying', 'copy_source_job': self.source})
        with patch.object(RUNNER, 'completion_service_active', side_effect=[True, False]*3), patch.object(RUNNER, 'run') as stop:
            RUNNER.completion_stop(self.destination)
        self.assertIn(['/usr/bin/systemctl', 'stop', RUNNER.completion_copy_unit(self.destination, self.source, 'resume')], [call.args[0] for call in stop.call_args_list])

    def test_real_report_repair_resume_keeps_visible_transport_out_of_baseline(self):
        self.assert_real_cli()
        original = self.prepare()
        old_work = self.jobs / self.destination / 'work'
        (old_work / 'autonomy-report.json').write_text('{malformed')
        self.archive(self.destination)
        next_job = self.new_job({})
        RUNNER.completion_resume(next_job, self.destination)
        work = self.jobs / next_job / 'work'
        self.assertEqual((work / 'autonomy-report.json').read_text(), '{malformed')
        self.assertFalse((self.jobs / next_job / 'completion/baseline/autonomy-report.json').exists())
        (work / 'autonomy-report.json').write_text('{"corrected":true}')
        (work / 'WORKSHOP.md').write_text('Corrected documentary claims')
        self.archive(next_job)
        self.assertEqual(RUNNER.completion_execute(next_job), 0)
        receipt = RUNNER.completion_ready(self.jobs / next_job / 'completion')
        self.assertEqual(receipt['baseline_sha256'], original['baseline_sha256'])
        self.assertFalse((self.jobs / next_job / 'completion/derived/work/autonomy-report.json').exists())

    def test_real_publication_recovers_after_move_without_rebuilding(self):
        self.assert_real_cli()
        self.prepare()
        (self.jobs / self.destination / 'work/WORKSHOP.md').write_text('corrected claims')
        self.archive(self.destination)
        original_write = RUNNER.completion_write
        def fail_ready(path, value):
            if path.name == 'receipt.json' and value.get('state') == 'ready':
                raise OSError('crash after public archive rename')
            return original_write(path, value)
        with patch.object(RUNNER, 'completion_write', fail_ready):
            self.assertEqual(RUNNER.completion_execute(self.destination), 1)
        public = self.jobs / self.destination / 'completion-artifact.tar.gz'
        self.assertTrue(public.exists())
        digest = RUNNER.digest_file(public)
        with patch.object(RUNNER, 'completion_extract_archive', side_effect=AssertionError('published result must not be rebuilt')):
            self.assertEqual(RUNNER.completion_execute(self.destination), 0)
        self.assertEqual(RUNNER.digest_file(public), digest)
        self.assertEqual(public.stat().st_mode & 0o777, 0o440)

    @unittest.skipUnless(os.geteuid() == 0, 'requires root for real admin permission boundary')
    def test_real_admin_can_read_public_archive_but_not_private_baseline(self):
        self.assert_real_cli()
        with patch.object(RUNNER.os, 'chown', REAL_CHOWN):
            self.prepare()
            (self.jobs / self.destination / 'work/WORKSHOP.md').write_text('corrected claims')
            self.archive(self.destination)
            self.assertEqual(RUNNER.completion_execute(self.destination), 0)
        self.root.chmod(0o755)
        self.jobs.chmod(0o755)
        job = self.jobs / self.destination
        admin = RUNNER.pwd.getpwnam('admin')
        REAL_CHOWN(job, 0, admin.pw_gid)
        job.chmod(0o750)
        public = job / 'completion-artifact.tar.gz'
        self.assertEqual(public.stat().st_uid, 0)
        self.assertEqual(public.stat().st_gid, admin.pw_gid)
        probe = """from pathlib import Path
import sys
archive, baseline = map(Path, sys.argv[1:])
assert archive.read_bytes()
for path, mode in [(archive, 'ab'), (baseline, 'rb'), (baseline, 'ab')]:
    try:
        path.open(mode).close()
    except PermissionError:
        continue
    raise AssertionError('unexpected access: ' + str(path) + ' ' + mode)
"""
        result = subprocess.run(['/usr/sbin/runuser', '-u', 'admin', '--', '/usr/bin/python3', '-c', probe, str(public), str(job / 'completion/baseline/WORKSHOP.md')], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_ordinary_copy_preserves_directory_modes_and_inherited_review_tree(self):
        source = self.jobs / self.source / 'work'
        source.chmod(0o750)
        evidence = source / '.lectern-review' / self.reviewer / 'work'
        evidence.mkdir(parents=True)
        (evidence / 'private').mkdir(mode=0o700)
        (evidence / 'public').mkdir(mode=0o755)
        (evidence / 'public/proof.txt').write_text('unchanged evidence')
        (evidence / 'public/proof.txt').chmod(0o444)
        (evidence / 'public').chmod(0o555)
        before = RUNNER.completion_inspect(source / '.lectern-review')
        for mask in (0o077, 0o022):
            destination = self.new_job({})
            previous = os.umask(mask)
            try:
                RUNNER.copy_job(destination, self.source)
            finally:
                os.umask(previous)
            copied = self.jobs / destination / 'work'
            self.assertEqual(copied.stat().st_mode & 0o777, 0o750)
            self.assertEqual(RUNNER.completion_inspect(copied / '.lectern-review'), before)
            self.assertEqual((copied / '.lectern-review' / self.reviewer / 'work/private').stat().st_mode & 0o777, 0o700)
            self.assertEqual((copied / '.lectern-review' / self.reviewer / 'work/public').stat().st_mode & 0o777, 0o555)

    def test_real_overlay_derived_reviewer_and_raw_archive_preserved(self):
        self.assert_real_cli()
        initial = self.prepare()
        source_archive = self.jobs / self.source / 'artifact.tar.gz'
        source_digest = RUNNER.digest_file(source_archive)
        work = self.jobs / self.destination / 'work'
        (work / 'WORKSHOP.md').write_text('Corrected documentary claims\n')
        (work / '.lectern-completion').mkdir(mode=0o755)
        (work / '.lectern-completion/provenance.md').write_text('Historical provenance correction\n')
        (work / 'autonomy-report.json').write_bytes(b'{"new":true}\r\n')
        raw_archive = self.archive(self.destination)
        raw_digest = RUNNER.digest_file(raw_archive)
        (work / 'code.py').write_text('mutable post-export change must never reach review')
        self.assertEqual(RUNNER.completion_execute(self.destination), 0)
        stage = self.jobs / self.destination / 'completion'
        receipt = RUNNER.completion_ready(stage)
        self.assertEqual(receipt['baseline_sha256'], initial['baseline_sha256'])
        self.assertEqual(RUNNER.digest_file(raw_archive), raw_digest)
        self.assertEqual(RUNNER.digest_file(source_archive), source_digest)
        self.assertEqual((stage / 'submission.json').read_bytes(), b'{"new":true}\r\n')
        reviewer = self.new_job({})
        with patch.object(RUNNER, 'completion_service_active', return_value=False), patch.object(RUNNER, 'run') as launch:
            self.assertEqual(RUNNER.completion_copy_status(reviewer, self.destination, 'derived')['state'], 'copying')
            self.assertIn('_copy-derived', launch.call_args.args[0])
        self.assertEqual(RUNNER.completion_copy_execute(reviewer, self.destination, 'derived'), 0)
        copied = RUNNER.completion_copy_derived(reviewer, self.destination)
        self.assertEqual(copied, RUNNER.completion_copy_derived(reviewer, self.destination))
        with patch.object(RUNNER, 'completion_service_active', return_value=False), patch.object(RUNNER, 'completion_inspect', side_effect=AssertionError('derived poll must not scan tree')):
            self.assertEqual(RUNNER.completion_copy_status(reviewer, self.destination, 'derived')['state'], 'copied')
        target = self.jobs / reviewer / 'work'
        self.assertEqual(RUNNER.completion_inspect(target), receipt['derived_tree_sha256'])
        self.assertEqual((target / '.lectern-completion/original-WORKSHOP.md').read_text(), 'original claims\n')
        self.assertEqual((target / 'code.py').read_text(), 'assert True\n')
        self.assertFalse((target / 'autonomy-report.json').exists())
        self.assertFalse((self.jobs / reviewer / 'completion/baseline.json').exists())

    def test_real_production_edit_rejected_without_rewriting_raw(self):
        self.assert_real_cli()
        self.prepare()
        work = self.jobs / self.destination / 'work'
        (work / 'code.py').write_text('assert False\n')
        raw = self.archive(self.destination); before = RUNNER.digest_file(raw)
        self.assertEqual(RUNNER.completion_execute(self.destination), 2)
        receipt = RUNNER.completion_json(self.jobs / self.destination / 'completion/receipt.json')
        self.assertEqual(receipt['state'], 'rejected')
        self.assertEqual(RUNNER.digest_file(raw), before)
        self.assertEqual(RUNNER.completion_execute(self.destination), 2)


if __name__ == '__main__':
    unittest.main()
