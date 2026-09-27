#!/usr/bin/python3
"""Fixed offline wheel resolver/validator. Never imports downloaded code on host.

resolve/install run in an empty network-isolated namespace with only the narrow
PyPI broker socket; probe runs in a SECOND namespace without that socket. Root
uses the same data-only wheel/file verifier before publishing immutable content.
"""
import argparse
import base64
import csv
import email.parser
import hashlib
import importlib.metadata
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import sys
import urllib.request
import zipfile

POLICY = 'pypi-pure-wheel-v1'
MAX_WHEEL = 64 * 1024**2
MAX_DOWNLOAD = 384 * 1024**2
MAX_EXTRACTED = 1024**3
MAX_FILES = 100000
MAX_DISTS = 48
INDEX = 'http://127.0.0.1:18080/python/simple/'

# Fixed pip is allowed to classify its own resolver exception, never package
# prose. A failed registry request makes that classification transient: pip
# otherwise turns an unreachable index into the same DistributionNotFound.
PIP_DOWNLOAD = r'''
import sys
sys.path.insert(0,sys.argv.pop(1))
from pip._internal.cli.main import main
from pip._internal.network.session import PipSession
from pip._internal.exceptions import DistributionNotFound
failed_requests=[]
original_send=PipSession.send
def send(self,request,**kwargs):
    try:
        response=original_send(self,request,**kwargs)
        authoritative_missing=(response.status_code==404 and
            request.url.startswith('http://127.0.0.1:18080/python/simple/') and
            response.headers.get('X-Lectern-Python-Registry')=='package-not-found')
        if response.status_code != 200 and not authoritative_missing: failed_requests.append(response.status_code)
        return response
    except Exception:
        failed_requests.append('transport')
        raise
PipSession.send=send
try:
    sys.exit(main())
except DistributionNotFound:
    if failed_requests:
        print('Registry transport unavailable; resolution is not a permanent absence',file=sys.stderr)
        sys.exit(1)
    print('Exact pins have no compatible pure-wheel resolution on this interpreter',file=sys.stderr)
    sys.exit(2)
'''


class Unsupported(ValueError):
    def __init__(self, message, code='unsupported'):
        super().__init__(message)
        self.code=code


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def sha(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def regular(path):
    info = Path(path).lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
        raise ValueError('not an independent regular file: ' + str(path))
    return info


def relative(name):
    value = PurePosixPath(name)
    if not name or value.is_absolute() or str(value) != name or '\\' in name or any(p in ('..', '.') for p in value.parts):
        raise ValueError('unsafe wheel path: ' + name)
    return value


def normalized(name):
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', name):
        raise ValueError('invalid distribution name')
    return re.sub(r'[-_.]+', '-', name).lower()


def metadata(data):
    return email.parser.BytesParser().parsebytes(data)


def snapshot_pip(source, destination):
    source, destination = Path(source), Path(destination)
    matches = list(source.glob('pip-*.dist-info'))
    if len(matches) != 1 or matches[0].is_symlink():
        raise ValueError('one trusted installed pip distribution required')
    dist = matches[0]
    record = dist / 'RECORD'; regular(record)
    rows = list(csv.reader(io.StringIO(record.read_text())))
    files = {}
    for row in rows:
        if len(row) != 3: raise ValueError('malformed pip RECORD')
        name, checksum, size = row
        if name in ('../../../bin/pip', '../../../bin/pip3', '../../../bin/pip3.13'):
            continue
        path = relative(name)
        if '__pycache__' in path.parts and path.suffix == '.pyc': continue
        if path.parts[0] not in ('pip', dist.name): raise ValueError('unexpected pip distribution file')
        actual = source / name
        for parent in (actual, *actual.parents):
            if parent == source: break
            if parent.is_symlink(): raise ValueError('linked pip tooling')
        info = regular(actual)
        if info.st_size > 16*1024**2: raise ValueError('oversize pip tool file')
        content = actual.read_bytes()
        digest = hashlib.sha256(content).digest()
        if name != dist.name + '/RECORD':
            expected = 'sha256=' + base64.urlsafe_b64encode(digest).decode().rstrip('=')
            if checksum != expected or size != str(len(content)): raise ValueError('pip installed RECORD mismatch')
        elif checksum or size:
            raise ValueError('unexpected pip RECORD self checksum')
        if name in files: raise ValueError('duplicate pip file')
        files[name] = content
    if 'pip/__init__.py' not in files or dist.name + '/METADATA' not in files:
        raise ValueError('pip tooling inventory incomplete')
    for tree in (source / 'pip', dist):
        for item in tree.rglob('*'):
            if item.is_symlink(): raise ValueError('linked pip tooling')
            if item.is_dir() or ('__pycache__' in item.parts and item.suffix == '.pyc'): continue
            if item.relative_to(source).as_posix() not in files: raise ValueError('unrecorded pip tooling')
    if destination.exists(): raise ValueError('pip tooling destination exists')
    destination.mkdir(parents=True)
    for name, data in files.items():
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data); target.chmod(0o444)
    for item in (destination, *destination.rglob('*')):
        if item.is_dir(): item.chmod(0o555)
    return {'record_sha256': sha(record), 'version': metadata(files[dist.name+'/METADATA'])['Version']}


def validate_requirements(request):
    # Vendored packaging is part of the trusted pip snapshot, never a project
    # import. This function only runs inside the resolver namespace.
    from pip._vendor.packaging.requirements import Requirement
    from pip._vendor.packaging.version import Version
    roots = []
    seen = {}
    for text in request['requirements'] + request['tooling_requirements']:
        parsed = Requirement(text)
        specs = list(parsed.specifier)
        if parsed.url or len(specs) != 1 or specs[0].operator != '==' or '*' in specs[0].version:
            raise Unsupported('exact == version pins only; no URLs, paths, ranges or editable installs')
        Version(specs[0].version)
        name = normalized(parsed.name)
        version = str(Version(specs[0].version))
        if len(parsed.extras) > 8: raise Unsupported('too many package extras')
        if parsed.marker is not None and not parsed.marker.evaluate():
            continue
        if name in seen and seen[name] != version: raise Unsupported('conflicting exact root package versions: '+name)
        seen[name] = version
        roots.append(str(parsed))
    if not roots or len(seen) > MAX_DISTS: raise Unsupported('empty or excessive dependency request')
    return sorted(set(roots))


def wheel_info(path):
    path = Path(path)
    if regular(path).st_size > MAX_WHEEL: raise Unsupported('wheel exceeds 64 MiB')
    digest = sha(path)
    with zipfile.ZipFile(path) as archive:
        members = archive.infolist()
        if len(members) > MAX_FILES: raise Unsupported('wheel file count exceeded')
        names, total = set(), 0
        for item in members:
            name = item.filename.rstrip('/') if item.is_dir() else item.filename
            relative(name)
            if name in names: raise ValueError('duplicate wheel member')
            names.add(name)
            mode = item.external_attr >> 16
            if stat.S_ISLNK(mode) or (stat.S_IFMT(mode) not in (0, stat.S_IFREG, stat.S_IFDIR)) or mode & 0o7000:
                raise ValueError('wheel link or special file/mode')
            if item.flag_bits & 1: raise Unsupported('encrypted wheels unsupported')
            total += item.file_size
            if total > MAX_EXTRACTED: raise Unsupported('wheel expansion limit exceeded')
        metas = [n for n in names if re.fullmatch(r'[^/]+\.dist-info/METADATA', n)]
        if len(metas) != 1: raise ValueError('wheel must have one top-level distribution metadata')
        prefix = metas[0].rsplit('/',1)[0]
        info = metadata(archive.read(metas[0]))
        name, version = normalized(info['Name']), info['Version']
        if not re.fullmatch(r'[A-Za-z0-9.!+_-]+', version): raise ValueError('unsafe wheel version')
        wh = metadata(archive.read(prefix + '/WHEEL'))
        if wh.get('Wheel-Version') != '1.0' or wh.get('Root-Is-Purelib', '').lower() != 'true':
            raise Unsupported('only Wheel 1.0 purelib distributions supported')
        tags = wh.get_all('Tag', [])
        if not tags or any(not re.fullmatch(r'py[0-9]+(?:\.py[0-9]+)*-none-any', tag) for tag in tags):
            raise Unsupported('only universal Python wheels supported')
        # Filename must agree with metadata and universal tags, not just WHEEL.
        filename = path.name
        bits = filename[:-4].split('-') if filename.endswith('.whl') else []
        if len(bits) not in (5,6) or normalized(bits[0]) != name or bits[1].replace('_','-') != version.replace('_','-') or bits[-2:] != ['none','any'] or not re.fullmatch(r'py[0-9]+(?:\.py[0-9]+)*', bits[-3]):
            raise ValueError('wheel filename/metadata identity mismatch')
        record = prefix + '/RECORD'
        recorded = {}
        for row in csv.reader(io.StringIO(archive.read(record).decode())):
            if len(row) != 3 or row[0] in recorded: raise ValueError('invalid wheel RECORD')
            relative(row[0]); recorded[row[0]] = row[1:]
        actual_files = {i.filename for i in members if not i.is_dir()}
        if actual_files != set(recorded): raise ValueError('wheel RECORD inventory mismatch')
        installed = []
        for original in sorted(actual_files):
            checksum, size = recorded[original]
            content = archive.read(original)
            if original == record:
                if checksum or size: raise ValueError('wheel RECORD self entry must be unhashed')
            else:
                expected = 'sha256=' + base64.urlsafe_b64encode(hashlib.sha256(content).digest()).decode().rstrip('=')
                if checksum != expected or size != str(len(content)): raise ValueError('wheel RECORD checksum mismatch')
            target = original
            components = original.split('/')
            if components[0].endswith('.data'):
                raise Unsupported('wheel .data relocation unsupported')
            relative(target)
            if target.endswith(('.pth','.pyc','.pyo','.so','.pyd','.dll','.dylib')) or target in ('sitecustomize.py','usercustomize.py') or content.startswith((b'\x7fELF', b'MZ')):
                raise Unsupported('startup hooks, compiled code and native payloads unsupported')
            installed.append({'path': target, 'original': original, 'sha256': hashlib.sha256(content).hexdigest(), 'size':len(content)})
        return {'name':name, 'version':version, 'filename':filename, 'sha256':digest,
                'bytes':path.stat().st_size, 'requires_python':info.get('Requires-Python',''),
                'requires_dist':info.get_all('Requires-Dist',[]), 'files':installed,
                'route':f'/python/wheel/{name}/{version}/{digest}/{filename}'}


def resolve(fetch, tooling):
    fetch, tooling = Path(fetch), Path(tooling)
    sys.path.insert(0,str(tooling))
    request = json.loads((fetch/'input.json').read_text())
    roots = validate_requirements(request)
    wheels = fetch/'wheels'; wheels.mkdir(exist_ok=True)
    lock_path = fetch/'lock.json'
    if lock_path.exists():
        lock = json.loads(lock_path.read_text())
        if lock.get('input_key') != request['input_key'] or lock.get('roots') != roots or lock.get('policy') != POLICY or not 0 < len(lock.get('wheels',[])) <= MAX_DISTS:
            raise ValueError('frozen lock input mismatch')
        for row in lock['wheels']:
            name = row['filename']; relative(name)
            if '/' in name: raise ValueError('invalid locked wheel basename')
            target = wheels/name
            if target.exists() and sha(target)==row['sha256']: continue
            expected = f"/python/wheel/{row['name']}/{row['version']}/{row['sha256']}/{name}"
            if row['route'] != expected: raise ValueError('invalid locked registry route')
            with urllib.request.urlopen('http://127.0.0.1:18080'+expected, timeout=90) as response:
                data = response.read(MAX_WHEEL+1)
            if len(data)>MAX_WHEEL or hashlib.sha256(data).hexdigest()!=row['sha256']: raise ValueError('locked wheel checksum mismatch')
            target.write_bytes(data)
    else:
        # Partial downloads from a failed, never-locked resolution are disposable.
        shutil.rmtree(wheels); wheels.mkdir()
        # Only fixed pip tooling executes; download cannot invoke build hooks
        # because no source distributions are eligible for this target.
        command = [sys.executable,'-I','-S','-c',PIP_DOWNLOAD,str(tooling),'--isolated','--debug','download','--disable-pip-version-check','--no-cache-dir','--retries','1','--timeout','20',
                   '--only-binary=:all:','--platform','any','--implementation','py','--abi','none',
                   '--python-version','.'.join(map(str,sys.version_info[:3])), '--index-url', INDEX,
                   '--dest',str(wheels),*roots]
        result = subprocess.run(command, check=False)
        if result.returncode == 2: raise Unsupported('exact pins have no compatible pure-wheel resolution on this interpreter')
        if result.returncode: raise RuntimeError('pip resolution unavailable; inspect resolver log')
        entries = [wheel_info(p) for p in sorted(wheels.iterdir())]
        if len(entries)>MAX_DISTS or sum(r['bytes'] for r in entries)>MAX_DOWNLOAD: raise Unsupported('resolved wheel budget exceeded')
        if len({r['name'] for r in entries})!=len(entries): raise ValueError('multiple versions of distribution')
        lock = {'schema_version':1,'policy':POLICY,'input_key':request['input_key'],'roots':roots,'wheels':entries}
        temporary = fetch/'lock.tmp'; temporary.write_bytes(canonical(lock)); temporary.replace(lock_path)
    # Revalidate every byte and metadata even after restoring a frozen lock.
    for row in lock['wheels']:
        if wheel_info(wheels/row['filename']) != row: raise ValueError('resolved wheel differs from frozen lock')
    print(json.dumps({'state':'locked','lock_sha256':sha(lock_path)}))


def install(fetch):
    fetch=Path(fetch); lock=json.loads((fetch/'lock.json').read_text())
    target=fetch/'site-packages'
    if target.exists(): shutil.rmtree(target)
    target.mkdir()
    inventory=[]; seen=set(); total=0
    for row in lock['wheels']:
        path=fetch/'wheels'/row['filename']
        if wheel_info(path)!=row: raise ValueError('installation wheel differs from lock')
        with zipfile.ZipFile(path) as archive:
            for file in row['files']:
                name=file['path']
                if name in seen: raise Unsupported('distribution installation path collision: '+name)
                seen.add(name); total+=file['size']
                if len(seen)>MAX_FILES or total>MAX_EXTRACTED: raise Unsupported('installed bundle budget exceeded')
                item=target/name; item.parent.mkdir(parents=True,exist_ok=True)
                data=archive.read(file['original']);item.write_bytes(data)
                inventory.append({'path':name,'sha256':hashlib.sha256(data).hexdigest(),'size':len(data)})
    (fetch/'files.json').write_bytes(canonical(sorted(inventory,key=lambda row:row['path'])))


def probe(fetch, output):
    # Called only in a credential-free namespace WITHOUT a network/registry
    # socket, project source or host home. Import code cannot reach the host.
    import importlib
    fetch=Path(fetch);request=json.loads((fetch/'input.json').read_text())
    sys.path.insert(0,str(fetch/'site-packages'))
    modules=sorted(set(request['imports']+['pytest']))
    observations=[]
    for name in modules:
        if not re.fullmatch(r'[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*',name): raise ValueError('invalid import probe')
        try:
            module=importlib.import_module(name)
        except Exception as error:
            raise Unsupported('Cannot import '+name+': '+type(error).__name__+': '+str(error)[:600],code='import_unavailable') from error
        origin=getattr(module,'__file__',None)
        if not origin or not str(origin).startswith(str(fetch/'site-packages')+'/'):
            raise ValueError('probe imported outside provided bundle: '+name)
        observations.append({'module':name,'origin':str(origin).replace(str(fetch),'/bundle')})
    Path(output).write_bytes(canonical({'schema_version':1,'input_key':request['input_key'],
        'imports':observations,'python_version':'.'.join(map(str,sys.version_info[:3])),'network':'unshared-no-socket'}))


def verify_install(fetch):
    fetch=Path(fetch); lock=json.loads((fetch/'lock.json').read_text())
    expected={}; packages={}
    if lock.get('policy')!=POLICY or len(lock.get('wheels',[]))>MAX_DISTS:
        raise ValueError('invalid installation lock')
    for row in lock['wheels']:
        if wheel_info(fetch/'wheels'/row['filename']) != row: raise ValueError('post-install wheel identity mismatch')
        packages[row['name']]=row['version']
        for item in row['files']:
            name=item['path']
            if name in expected: raise ValueError('installed collision')
            expected[name]={'path':name,'size':item['size'],'sha256':item['sha256']}
    site=fetch/'site-packages'; actual=set();total=0
    for path in site.rglob('*'):
        if path.is_symlink(): raise ValueError('linked installed path')
        if path.is_dir(): continue
        info=regular(path);name=path.relative_to(site).as_posix();actual.add(name)
        entry=expected.get(name);total+=info.st_size
        if not entry or entry['size']!=info.st_size or entry['sha256']!=sha(path):raise ValueError('installed bytes differ from verified wheel')
    if actual!=set(expected) or total>MAX_EXTRACTED:raise ValueError('installed inventory differs from lock')
    return {'packages':packages,'files':[expected[name] for name in sorted(expected)],'lock_sha256':sha(fetch/'lock.json')}


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command',choices=['snapshot-pip','resolve','install','probe','verify'])
    parser.add_argument('--fetch',default='/fetch')
    parser.add_argument('--tooling',default='/tooling')
    parser.add_argument('--proof', default='/proof/probe.json')
    parser.add_argument('--source')
    parser.add_argument('--destination')
    args=parser.parse_args()
    if args.command=='snapshot-pip':print(json.dumps(snapshot_pip(args.source,args.destination)))
    elif args.command=='resolve':resolve(args.fetch,args.tooling)
    elif args.command=='install':install(args.fetch)
    elif args.command=='verify':print(json.dumps(verify_install(args.fetch)))
    else:probe(args.fetch,args.proof)


if __name__=='__main__':
    try:main()
    except (ValueError, zipfile.BadZipFile) as error:
        print(json.dumps({'error':str(error),'code':getattr(error,'code','unsupported')}),file=sys.stderr);sys.exit(2)
