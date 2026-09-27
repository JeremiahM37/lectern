#!/usr/bin/env python3
"""Fixed-target read-only GPU screening. No lease, launch, stop or sysfs writes.

Receipt hashes attest captured input bytes, not a hardware trust root. Only the
trusted supervisor may supply policy/expected identity or consume these facts.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import time

TARGET = 'aiserver-amd-research-v1'
BDF = '0000:f4:00.0'
GUEST = 105
GIB = 1024 ** 3
DEFAULT_POLICY = {
    'schema_version': 1, 'profile': 'gpu-screen600',
    'abort_millicelsius': 80000, 'rearm_millicelsius': 70000,
    'launch_busy_max_percent': 20, 'minimum_host_available_bytes': 8 * GIB,
    'minimum_gtt_headroom_bytes': 8 * GIB,
    'minimum_vram_headroom_bytes': 256 * 1024 ** 2,
    'maximum_age_seconds': 5,
}
LIMITATIONS = [
    'Screening guards only; runtime and hardware qualification are separate.',
    'GPU/GTT usage is device-global, not attributable to the owned workload.',
    'No hard per-job GPU memory cap or GPU reset containment is established.',
    'GTT and host available memory overlap; do not add these headrooms.',
    'Guest cgroup memory.current includes reclaimable cache; raw headroom is not usable-memory prediction.',
    'Temperature covers only exposed sensors; an absent hotspot/critical limit is not inferred.',
    'Driver digest covers recorded kernel/module identity metadata, not all driver binary bytes.',
    'Samples are bracketed but counters are not an atomic hardware snapshot.',
]

class Unknown(ValueError):
    pass


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()).hexdigest()


def policy_checked(policy):
    if not isinstance(policy, dict) or set(policy) != set(DEFAULT_POLICY):
        raise Unknown('unsupported_policy_fields')
    if policy['schema_version'] != 1 or policy['profile'] != 'gpu-screen600':
        raise Unknown('unsupported_policy_profile')
    for key in set(policy) - {'profile'}:
        if type(policy[key]) is not int:
            raise Unknown('invalid_policy_number')
    if not 40000 <= policy['rearm_millicelsius'] < policy['abort_millicelsius'] <= 80000:
        raise Unknown('invalid_thermal_policy')
    if not 0 <= policy['launch_busy_max_percent'] <= 20 or not 1 <= policy['maximum_age_seconds'] <= 5:
        raise Unknown('invalid_screening_policy')
    for key in ('minimum_host_available_bytes', 'minimum_gtt_headroom_bytes', 'minimum_vram_headroom_bytes'):
        if not DEFAULT_POLICY[key] <= policy[key] <= 256 * GIB:
            raise Unknown('invalid_headroom_policy')
    return dict(policy)


class Reader:
    # Alternate roots exist for disposable tests, never CLI/worker inputs.
    def __init__(self, sys_root=Path('/sys'), proc_root=Path('/proc')):
        self.sys, self.proc, self.inputs = Path(sys_root), Path(proc_root), {}

    def read(self, path, limit=4096):
        flags = os.O_RDONLY | os.O_CLOEXEC | os.O_NONBLOCK | os.O_NOFOLLOW
        try:
            fd = os.open(path, flags)
            try:
                if not stat.S_ISREG(os.fstat(fd).st_mode):
                    raise Unknown('nonregular_sensor')
                raw = os.read(fd, limit + 1)
            finally:
                os.close(fd)
            if len(raw) > limit:
                raise Unknown('oversized_sensor')
            text = raw.decode('ascii').strip()
            if not text:
                raise Unknown('empty_sensor')
            root, label = (self.sys, 'sys') if path.is_relative_to(self.sys) else (self.proc, 'proc')
            self.inputs[label + '/' + path.relative_to(root).as_posix()] = hashlib.sha256(raw).hexdigest()
            return text
        except (OSError, UnicodeError) as error:
            raise Unknown('sensor_unavailable:' + path.name) from error

    def integer(self, path, minimum, maximum):
        text = self.read(path)
        if not re.fullmatch(r'-?[0-9]{1,20}', text):
            raise Unknown('invalid_sensor_number:' + path.name)
        value = int(text)
        if not minimum <= value <= maximum:
            raise Unknown('sensor_out_of_range:' + path.name)
        return value

    def identity(self):
        device = self.sys / 'bus/pci/devices' / BDF
        ids = {name: self.read(device / name) for name in ('vendor', 'device', 'revision')}
        if ids != {'vendor': '0x1002', 'device': '0x1586', 'revision': '0xc1'}:
            raise Unknown('unexpected_physical_device')
        try:
            driver = (device / 'driver').resolve(strict=True)
        except OSError as error:
            raise Unknown('driver_unavailable') from error
        if driver != (self.sys / 'bus/pci/drivers/amdgpu').resolve(strict=True):
            raise Unknown('unexpected_driver')
        release = self.read(self.proc / 'sys/kernel/osrelease')
        srcversion = self.read(self.sys / 'module/amdgpu/srcversion')
        boot = self.read(self.proc / 'sys/kernel/random/boot_id')
        if not re.fullmatch(r'[a-zA-Z0-9._+~-]{1,128}', release) or not re.fullmatch(r'[A-Fa-f0-9]{16,64}', srcversion):
            raise Unknown('invalid_kernel_driver_identity')
        if not re.fullmatch(r'[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}', boot):
            raise Unknown('invalid_boot_identity')
        render = self.read(device / 'drm/renderD128/dev')
        kfd = self.read(self.sys / 'class/kfd/kfd/dev')
        if kfd != '234:0':
            raise Unknown('unexpected_compute_device')
        if render != '226:128':
            raise Unknown('unexpected_render_device')
        return dict(ids, device_bdf=BDF, guest=GUEST, render_device='226:128', kfd_device=kfd, driver='amdgpu',
                    kernel_release=release, module_srcversion=srcversion, boot_id=boot)


def observe(policy=None, reader=None):
    """Produce one bounded observation, unavailable on any required unknown."""
    policy = policy_checked(DEFAULT_POLICY if policy is None else policy)
    reader = reader or Reader()
    start = time.monotonic_ns()
    result = {'schema_version': 1, 'target': TARGET, 'device_bdf': BDF, 'guest': GUEST,
              'observed_at_unix_ns': time.time_ns(), 'monotonic_ns': start,
              'policy': policy, 'policy_sha256': digest(policy), 'mutation_performed': False,
              'helper_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
              'qualification': 'screening_only', 'limitations': list(LIMITATIONS)}
    try:
        identity = reader.identity()
        device = reader.sys / 'bus/pci/devices' / BDF
        sensors = []
        hwmons = sorted((device / 'hwmon').glob('hwmon*'))
        if len(hwmons) != 1 or reader.read(hwmons[0] / 'name') != 'amdgpu':
            raise Unknown('ambiguous_or_missing_temperature_device')
        inputs = sorted(hwmons[0].glob('temp*_input'))
        if not inputs or len(inputs) > 8:
            raise Unknown('temperature_unavailable')
        for path in inputs:
            if not re.fullmatch(r'temp[1-9][0-9]?_input', path.name):
                raise Unknown('unsupported_temperature_sensor')
            label = reader.read(path.with_name(path.name.replace('_input', '_label')))
            if not re.fullmatch(r'[A-Za-z0-9 _-]{1,40}', label):
                raise Unknown('invalid_temperature_label')
            sensors.append({'sensor': path.name, 'label': label,
                            'millicelsius': reader.integer(path, -40000, 150000)})
        facts = {'temperature_sensors': sensors, 'temperature_max_millicelsius': max(x['millicelsius'] for x in sensors),
                 'gpu_busy_percent': reader.integer(device / 'gpu_busy_percent', 0, 100)}
        for kind in ('gtt', 'vram'):
            total = reader.integer(device / ('mem_info_' + kind + '_total'), 1, 1024 * GIB)
            used = reader.integer(device / ('mem_info_' + kind + '_used'), 0, total)
            facts[kind] = {'total_bytes': total, 'used_bytes': used, 'headroom_bytes': total - used}
        meminfo = reader.read(reader.proc / 'meminfo', 65536)
        mem = {}
        for field in ('MemTotal', 'MemAvailable'):
            matches = re.findall(r'^' + field + r':\s+([0-9]+) kB$', meminfo, re.M)
            if len(matches) != 1:
                raise Unknown('host_memory_unavailable')
            mem[field] = int(matches[0]) * 1024
        if not 0 <= mem['MemAvailable'] <= mem['MemTotal'] <= 4096 * GIB:
            raise Unknown('invalid_host_memory')
        facts['host_memory'] = {'total_bytes': mem['MemTotal'], 'available_bytes': mem['MemAvailable']}
        # Optional fact, never promoted into a per-job GPU accounting assertion.
        try:
            group = reader.sys / 'fs/cgroup/lxc/105'
            current = reader.integer(group / 'memory.current', 0, 4096 * GIB)
            maximum = reader.integer(group / 'memory.max', 1, 4096 * GIB)
            facts['guest_memory'] = {'state': 'observed', 'current_bytes': current, 'maximum_bytes': maximum,
                                     'raw_headroom_bytes': max(0, maximum - current), 'gpu_accounting': 'unqualified'}
        except Unknown as error:
            facts['guest_memory'] = {'state': 'unknown', 'reason': str(error), 'gpu_accounting': 'unqualified'}
        if reader.identity() != identity:
            raise Unknown('identity_changed_during_observation')
        result.update(state='observed', identity=identity, identity_sha256=digest(identity),
                      kernel_release=identity['kernel_release'],
                      driver_sha256=digest({k: identity[k] for k in ('driver', 'kernel_release', 'module_srcversion')}), facts=facts)
    except (Unknown, OSError) as error:
        result.update(state='unavailable', reason=str(error)[:240])
    result['duration_ns'] = time.monotonic_ns() - start
    result['input_sha256'] = digest(reader.inputs)
    result['inputs'] = dict(reader.inputs)
    result['receipt_sha256'] = digest(result)
    return result


def evaluate(receipt, prior_latched=False, running=False, *, expected_identity_sha256=None,
             policy=None, now_ns=None, monotonic_ns=None):
    """A guard decision only. Caller owns qualification, lease and owned-unit stop."""
    latched = bool(prior_latched)
    def deny(reason, thermal=False):
        return {'decision': 'abort' if running else 'wait', 'reason': reason,
                'latched': latched or thermal, 'qualification': 'screening_only'}
    try:
        selected = policy_checked(DEFAULT_POLICY if policy is None else policy)
        if not isinstance(receipt, dict) or digest({k: v for k, v in receipt.items() if k != 'receipt_sha256'}) != receipt.get('receipt_sha256'):
            return deny('invalid_receipt_identity')
        if receipt.get('schema_version') != 1 or receipt.get('target') != TARGET or receipt.get('device_bdf') != BDF or receipt.get('guest') != GUEST or receipt.get('mutation_performed') is not False:
            return deny('foreign_observation')
        if receipt.get('state') != 'observed':
            return deny('telemetry_unavailable')
        if receipt['policy'] != selected or receipt['policy_sha256'] != digest(selected):
            return deny('policy_changed')
        if digest(receipt['identity']) != receipt['identity_sha256'] or (expected_identity_sha256 is not None and receipt['identity_sha256'] != expected_identity_sha256):
            return deny('qualification_identity_changed')
        wall = time.time_ns() if now_ns is None else now_ns
        mono = time.monotonic_ns() if monotonic_ns is None else monotonic_ns
        maximum = selected['maximum_age_seconds'] * 10**9
        if any(type(x) is not int for x in (receipt['observed_at_unix_ns'], receipt['monotonic_ns'], receipt['duration_ns'])):
            return deny('invalid_observation_time')
        if not 0 <= wall - receipt['observed_at_unix_ns'] <= maximum or not 0 <= mono - receipt['monotonic_ns'] <= maximum or not 0 <= receipt['duration_ns'] <= maximum:
            return deny('stale_observation')
        if digest(receipt['inputs']) != receipt['input_sha256'] or not re.fullmatch(r'[0-9a-f]{64}', receipt['helper_sha256']):
            return deny('invalid_observation_inputs')
        facts = receipt['facts']; temperature = facts['temperature_max_millicelsius']
        sensors = facts['temperature_sensors']
        if not sensors or len(sensors) > 8 or any(type(x['millicelsius']) is not int or not -40000 <= x['millicelsius'] <= 150000 for x in sensors):
            return deny('invalid_temperature_facts')
        if type(temperature) is not int or temperature != max(x['millicelsius'] for x in sensors):
            return deny('invalid_temperature_facts')
        busy = facts['gpu_busy_percent']
        if type(busy) is not int or not 0 <= busy <= 100:
            return deny('invalid_utilization_facts')
        for kind in ('gtt', 'vram'):
            memory = facts[kind]
            if any(type(memory[k]) is not int for k in ('total_bytes', 'used_bytes', 'headroom_bytes')) or not 0 <= memory['used_bytes'] <= memory['total_bytes'] <= 1024 * GIB or memory['headroom_bytes'] != memory['total_bytes'] - memory['used_bytes']:
                return deny('invalid_memory_facts')
        memory = facts['host_memory']
        if any(type(memory[k]) is not int for k in ('total_bytes', 'available_bytes')) or not 0 <= memory['available_bytes'] <= memory['total_bytes'] <= 4096 * GIB:
            return deny('invalid_memory_facts')
        if temperature >= selected['abort_millicelsius']:
            return deny('thermal_ceiling', True)
        if latched and temperature > selected['rearm_millicelsius']:
            return deny('thermal_cooldown')
        latched = False
        if facts['host_memory']['available_bytes'] < selected['minimum_host_available_bytes'] or facts['gtt']['headroom_bytes'] < selected['minimum_gtt_headroom_bytes'] or facts['vram']['headroom_bytes'] < selected['minimum_vram_headroom_bytes']:
            return deny('memory_headroom')
        if not running and facts['gpu_busy_percent'] > selected['launch_busy_max_percent']:
            return deny('ambient_busy')
        return {'decision': 'allow', 'reason': 'screening_guards_satisfied', 'latched': False,
                'qualification': 'screening_only', 'timing_qualified': False,
                'gpu_utilization_attribution': 'unknown' if running else 'prelaunch_global_observation'}
    except (KeyError, TypeError, ValueError, OverflowError):
        return deny('malformed_observation')


if __name__ == '__main__':
    import sys
    if len(sys.argv) != 1:
        raise SystemExit('This fixed read-only observer accepts no target/path arguments')
    observation = observe()
    print(json.dumps({'observation': observation, 'guard': evaluate(observation)}, sort_keys=True))
