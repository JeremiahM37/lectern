import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
import unittest

spec = importlib.util.spec_from_file_location('gpu_telemetry', Path(__file__).with_name('autonomy-gpu-telemetry.py'))
T = importlib.util.module_from_spec(spec)
spec.loader.exec_module(T)

class TelemetryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.sys, self.proc = self.root / 'sys', self.root / 'proc'
        self.device = self.sys / 'bus/pci/devices' / T.BDF
        values = {
            self.device / 'vendor': '0x1002', self.device / 'device': '0x1586',
            self.device / 'revision': '0xc1', self.device / 'gpu_busy_percent': '0',
            self.device / 'drm/renderD128/dev': '226:128',
            self.device / 'hwmon/hwmon5/name': 'amdgpu',
            self.device / 'hwmon/hwmon5/temp1_label': 'edge',
            self.device / 'hwmon/hwmon5/temp1_input': '43000',
            self.sys / 'module/amdgpu/srcversion': 'A' * 24,
            self.sys / 'class/kfd/kfd/dev': '234:0',
            self.proc / 'sys/kernel/osrelease': '6.17.13-2-pve',
            self.proc / 'sys/kernel/random/boot_id': '11111111-1111-1111-1111-111111111111',
            self.proc / 'meminfo': 'MemTotal:       134217728 kB\nMemAvailable:   67108864 kB',
        }
        for kind, total in [('gtt', 96 * T.GIB), ('vram', 2 * T.GIB)]:
            values[self.device / ('mem_info_' + kind + '_total')] = str(total)
            values[self.device / ('mem_info_' + kind + '_used')] = '0'
        for path, text in values.items():
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text + '\n')
        driver = self.sys / 'bus/pci/drivers/amdgpu'
        driver.mkdir(parents=True)
        (self.device / 'driver').symlink_to(driver, target_is_directory=True)
        self.reader = T.Reader(self.sys, self.proc)

    def tearDown(self):
        self.tmp.cleanup()

    def observe(self):
        return T.observe(reader=self.reader)

    def test_exact_identity_units_inputs_and_no_mutation(self):
        before = {str(p): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        receipt = self.observe()
        self.assertEqual(receipt['state'], 'observed')
        self.assertEqual(receipt['facts']['temperature_max_millicelsius'], 43000)
        self.assertEqual(receipt['facts']['host_memory']['available_bytes'], 64 * T.GIB)
        self.assertEqual(receipt['identity']['render_device'], '226:128')
        self.assertEqual(receipt['facts']['guest_memory']['state'], 'unknown')
        self.assertFalse(receipt['mutation_performed'])
        result = T.evaluate(receipt, expected_identity_sha256=receipt['identity_sha256'])
        self.assertEqual(result['decision'], 'allow')
        self.assertFalse(result['timing_qualified'])
        self.assertEqual(before, {str(p): p.read_bytes() for p in self.root.rglob('*') if p.is_file()})
        self.assertEqual(receipt['receipt_sha256'], T.digest({k: v for k, v in receipt.items() if k != 'receipt_sha256'}))

    def test_thermal_abort_and_hysteresis(self):
        sensor = self.device / 'hwmon/hwmon5/temp1_input'
        sensor.write_text('80000')
        hot = self.observe()
        self.assertEqual(T.evaluate(hot)['decision'], 'wait')
        self.assertEqual(T.evaluate(hot, running=True)['decision'], 'abort')
        self.assertTrue(T.evaluate(hot)['latched'])
        sensor.write_text('71000')
        self.assertEqual(T.evaluate(self.observe(), prior_latched=True)['reason'], 'thermal_cooldown')
        sensor.write_text('70000')
        self.assertEqual(T.evaluate(self.observe(), prior_latched=True)['decision'], 'allow')

    def test_own_running_utilization_is_not_external_contention(self):
        (self.device / 'gpu_busy_percent').write_text('100')
        receipt = self.observe()
        self.assertEqual(T.evaluate(receipt)['reason'], 'ambient_busy')
        running = T.evaluate(receipt, running=True)
        self.assertEqual(running['decision'], 'allow')
        self.assertFalse(running['timing_qualified'])
        self.assertEqual(running['gpu_utilization_attribution'], 'unknown')

    def test_each_headroom_gate_aborts_running_only_owned_caller_decides_stop(self):
        for field in ('host', 'gtt', 'vram'):
            with self.subTest(field=field):
                if field == 'host':
                    p = self.proc / 'meminfo'; old = p.read_text()
                    p.write_text('MemTotal: 134217728 kB\nMemAvailable: 1024 kB\n')
                else:
                    p = self.device / ('mem_info_' + field + '_used'); old = p.read_text()
                    p.write_text((self.device / ('mem_info_' + field + '_total')).read_text())
                receipt = self.observe()
                self.assertEqual(T.evaluate(receipt, running=True)['reason'], 'memory_headroom')
                self.assertEqual(T.evaluate(receipt, running=True)['decision'], 'abort')
                p.write_text(old)

    def test_missing_malformed_sensor_and_wrong_device_fail_closed(self):
        cases = [('gpu_busy_percent', '101'), ('gpu_busy_percent', 'NaN'),
                 ('mem_info_gtt_used', str(200 * T.GIB)), ('vendor', '0x10de'),
                 ('hwmon/hwmon5/temp1_input', '500000'), ('hwmon/hwmon5/temp1_input', '43C')]
        for rel, value in cases:
            p = self.device / rel; old = p.read_text(); p.write_text(value)
            receipt = self.observe()
            self.assertEqual(receipt['state'], 'unavailable', rel)
            self.assertEqual(T.evaluate(receipt, running=True)['decision'], 'abort', rel)
            p.write_text(old)
        (self.device / 'gpu_busy_percent').unlink()
        self.assertEqual(self.observe()['state'], 'unavailable')

    def test_fifo_symlink_and_oversize_sensor_never_block_or_read_target(self):
        path = self.device / 'gpu_busy_percent'
        path.unlink(); os.mkfifo(path)
        start = time.monotonic()
        self.assertEqual(self.observe()['state'], 'unavailable')
        self.assertLess(time.monotonic() - start, 1)
        path.unlink(); path.symlink_to(self.proc / 'meminfo')
        self.assertEqual(self.observe()['state'], 'unavailable')
        path.unlink(); path.write_text('0' * 5000)
        self.assertEqual(self.observe()['state'], 'unavailable')

    def test_qualification_drift_staleness_and_receipt_tamper(self):
        old = self.observe()
        (self.sys / 'module/amdgpu/srcversion').write_text('B' * 24)
        changed = self.observe()
        self.assertEqual(T.evaluate(changed, expected_identity_sha256=old['identity_sha256'])['reason'], 'qualification_identity_changed')
        self.assertEqual(T.evaluate(old, now_ns=old['observed_at_unix_ns'] + 6 * 10**9)['reason'], 'stale_observation')
        self.assertEqual(T.evaluate(old, monotonic_ns=old['monotonic_ns'] - 1)['reason'], 'stale_observation')
        old['facts']['temperature_max_millicelsius'] = 1
        self.assertEqual(T.evaluate(old)['reason'], 'invalid_receipt_identity')

    def test_bracket_detects_driver_change_during_sample(self):
        original = self.reader.identity
        count = 0
        def changing():
            nonlocal count
            count += 1
            result = original()
            if count == 2:
                result['kernel_release'] = 'different'
            return result
        self.reader.identity = changing
        receipt = self.observe()
        self.assertEqual(receipt['state'], 'unavailable')
        self.assertEqual(receipt['reason'], 'identity_changed_during_observation')

    def test_no_weaker_unknown_or_worker_target_policy(self):
        for changes in ({'abort_millicelsius': 90000}, {'rearm_millicelsius': 80000},
                        {'minimum_gtt_headroom_bytes': 1}, {'launch_busy_max_percent': 100},
                        {'target': '/arbitrary'}, {'schema_version': True}):
            with self.subTest(changes=changes), self.assertRaises(T.Unknown):
                T.observe(dict(T.DEFAULT_POLICY, **changes), self.reader)

    def test_resealed_malformed_facts_are_not_valid_telemetry(self):
        for field, value in [('gpu_busy_percent', True), ('temperature_max_millicelsius', 1)]:
            receipt = self.observe()
            receipt['facts'][field] = value
            receipt['receipt_sha256'] = T.digest({k: v for k, v in receipt.items() if k != 'receipt_sha256'})
            self.assertEqual(T.evaluate(receipt, running=True)['decision'], 'abort')

    def test_sensor_maximum_does_not_hide_hotspot(self):
        h = self.device / 'hwmon/hwmon5'
        (h / 'temp2_input').write_text('81000')
        (h / 'temp2_label').write_text('junction')
        receipt = self.observe()
        self.assertEqual(receipt['facts']['temperature_max_millicelsius'], 81000)
        self.assertEqual(T.evaluate(receipt, running=True)['decision'], 'abort')

if __name__ == '__main__':
    unittest.main()
