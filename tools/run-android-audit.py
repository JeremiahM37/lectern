#!/usr/bin/env python3
"""Run the native Android audit in disposable fixtures; never targets live Lectern."""
import argparse
import datetime
import fcntl
import json
import os
from pathlib import Path
import subprocess
import sys
import time


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--checkout',type=Path,default=Path(__file__).resolve().parents[1])
    parser.add_argument('--sdk',type=Path,default=Path(os.environ.get('ANDROID_SDK_ROOT',Path.home()/'android-sdk')))
    parser.add_argument('--avd',default='pixel7_test')
    parser.add_argument('--serial',default='emulator-5554')
    parser.add_argument('--artifacts',type=Path,default=Path.home()/'.local/state/lectern/android-audit')
    parser.add_argument('--preflight',action='store_true')
    args=parser.parse_args()
    if not args.serial.startswith('emulator-') or not args.serial.removeprefix('emulator-').isdigit():
        parser.error('Only an explicit emulator serial is allowed')
    checkout=args.checkout.resolve(); sdk=args.sdk.resolve()
    adb=sdk/'platform-tools/adb'; emulator=sdk/'emulator/emulator'
    for path in [adb,emulator,checkout/'tools/run-isolated-tests.sh',checkout/'tools/android-audit-fixture.py']:
        if not path.is_file(): parser.error(f'Missing dependency: {path}')
    if os.environ.get('ADK_ISOLATION_REVIEWED')!='1':
        parser.error('Read tools/run-isolated-tests.sh, then set ADK_ISOLATION_REVIEWED=1')
    if args.preflight:
        print('PASS: Android audit paths and explicit emulator serial validated');return
    state=Path.home()/'.local/state/lectern';state.mkdir(parents=True,exist_ok=True)
    lock=(state/'android-audit.lock').open('w')
    try:fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
    except BlockingIOError:raise SystemExit('An Android audit is already running')
    stamp=datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    out=args.artifacts.resolve()/stamp;out.mkdir(parents=True)
    result={'ok':False,'started_at':stamp,'checkout':str(checkout),'artifacts':str(out)}
    proc=None
    code=1
    try:
        subprocess.run([str(adb),'start-server'],check=True,stdout=subprocess.DEVNULL)
        rows=subprocess.check_output([str(adb),'devices'],text=True).splitlines()
        if not any(row.split()[:2]==[args.serial,'device'] for row in rows):
            if any(row.startswith(args.serial+'\t') for row in rows):
                raise RuntimeError('Requested emulator exists but is not ready; not taking it over')
            port=args.serial.removeprefix('emulator-')
            with (out/'emulator.log').open('w') as log:
                proc=subprocess.Popen([str(emulator),'-avd',args.avd,'-port',port,'-no-window',
                    '-no-audio','-no-boot-anim','-no-snapshot-save','-gpu','swiftshader_indirect'],stdout=log,stderr=log)
            for _ in range(150):
                if proc.poll() is not None:raise RuntimeError('Emulator exited; see emulator.log')
                ready=subprocess.run([str(adb),'-s',args.serial,'shell','getprop','sys.boot_completed'],capture_output=True,text=True,timeout=5)
                if ready.stdout.strip()=='1':break
                time.sleep(1)
            else:raise RuntimeError('Emulator boot timed out')
        env={**os.environ,'ADK_TEST_MODE':'android','ANDROID_SDK_ROOT':str(sdk),
             'LEC_ANDROID_SERIAL':args.serial,'LEC_ANDROID_ARTIFACTS':str(out)}
        with (out/'audit.log').open('w') as log:
            completed=subprocess.run([str(checkout/'tools/run-isolated-tests.sh'),str(checkout)],env=env,cwd=checkout,stdout=log,stderr=subprocess.STDOUT,timeout=1500)
        code=completed.returncode
        if (out/'result.json').exists():result.update(json.loads((out/'result.json').read_text()))
        result['ok']=code==0 and result.get('ok') is True
        if not result['ok'] and code==0:code=1
    except Exception as exc:
        result['error']=str(exc);code=1
    finally:
        if proc is not None:
            try:
                subprocess.run([str(adb),'-s',args.serial,'emu','kill'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=15)
            except (OSError, subprocess.TimeoutExpired):
                pass
            try:proc.wait(timeout=20)
            except subprocess.TimeoutExpired:proc.kill();proc.wait()
        result['exit_code']=code
        (out/'result.json').write_text(json.dumps(result,indent=2)+'\n')
        latest=args.artifacts.resolve()/'latest.json';partial=latest.with_suffix('.tmp')
        partial.write_text(json.dumps(result,indent=2)+'\n');partial.replace(latest)
        print(f'{"PASS" if result["ok"] else "FAIL"}: native Android audit — {out}')
        lock.close()
    raise SystemExit(code)

if __name__=='__main__':main()
