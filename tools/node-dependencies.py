#!/usr/bin/python3
"""Data-only npm lock/tarball validation for the workshop's offline provisioner.

This module grants no execution or network capability. Resolution, isolated
installation (including required hooks), runtime probes and controller receipts
must succeed separately before a bundle can be advertised as usable.
"""
import base64
import hashlib
import gzip
import io
import json
import re
import tarfile
from pathlib import PurePosixPath
from urllib.parse import urlsplit

MAX_PACKAGES = 512
MAX_TARBALL = 64 * 1024**2
MAX_UNPACKED = 512 * 1024**2
MAX_FILES = 50000
NAME = re.compile(r'(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*\Z')
VERSION = re.compile(r'(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?\Z')


def object_pairs(pairs):
    out = {}
    for key, value in pairs:
        if key in out:
            raise ValueError('duplicate JSON key')
        out[key] = value
    return out


def read_json(raw):
    if len(raw) > 8 * 1024**2:
        raise ValueError('oversize metadata')
    return json.loads(raw, object_pairs_hook=object_pairs)


def package_path(path):
    """Accept npm's nested/scoped package layout, never a workspace/link path."""
    if not isinstance(path, str):
        raise ValueError('package path must be a string')
    parts = path.split('/')
    names = []
    while parts:
        if parts.pop(0) != 'node_modules' or not parts:
            raise ValueError('invalid package path')
        name = parts.pop(0)
        if name.startswith('@'):
            if not parts:
                raise ValueError('incomplete scoped package')
            name += '/' + parts.pop(0)
        if not NAME.fullmatch(name) or name in ('.', '..'):
            raise ValueError('invalid package identity')
        names.append(name)
    if not names:
        raise ValueError('empty package path')
    return names[-1]


def integrity(value):
    if not isinstance(value, str) or not value.startswith('sha512-'):
        raise ValueError('sha512 integrity required')
    try:
        raw = base64.b64decode(value[7:], validate=True)
    except (ValueError, TypeError) as error:
        raise ValueError('malformed integrity') from error
    if len(raw) != 64 or base64.b64encode(raw).decode() != value[7:]:
        raise ValueError('noncanonical integrity')
    return raw


def registry_tarball(url, name, version):
    if not isinstance(url, str):
        raise ValueError('missing tarball URL')
    parsed = urlsplit(url)
    # Exact path prevents URL credentials, redirects, queries, encoded traversal,
    # external dependencies and arbitrary endpoints on the registry origin.
    expected = '/' + name + '/-/' + name.split('/')[-1] + '-' + version + '.tgz'
    if url != 'https://registry.npmjs.org' + expected or parsed.scheme != 'https' or parsed.netloc != 'registry.npmjs.org' or parsed.path != expected or parsed.query or parsed.fragment:
        raise ValueError('noncanonical public registry tarball')
    return url


def validate_lock(manifest_raw, lock_raw):
    manifest, lock = read_json(manifest_raw), read_json(lock_raw)
    if not isinstance(manifest, dict) or not isinstance(lock, dict):
        raise ValueError('metadata must be objects')
    if lock.get('lockfileVersion') != 3 or manifest.get('workspaces'):
        raise ValueError('version 3 non-workspace lock required')
    packages = lock.get('packages')
    if not isinstance(packages, dict) or '' not in packages or not 1 <= len(packages) <= MAX_PACKAGES + 1:
        raise ValueError('invalid package inventory')
    root = packages['']
    if not isinstance(root, dict):
        raise ValueError('invalid lock root')
    for key in ('dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies', 'peerDependenciesMeta'):
        if manifest.get(key, {}) != root.get(key, {}):
            raise ValueError('manifest/lock root mismatch: ' + key)
    rows = []
    for path, entry in packages.items():
        if path == '':
            continue
        name = package_path(path)
        if not isinstance(entry, dict) or entry.get('link') or entry.get('inBundle'):
            raise ValueError('linked or bundled package not supported')
        # Alias support needs explicit independent resolved-name binding; never
        # silently interpret a filesystem alias as a registry package identity.
        if entry.get('name', name) != name:
            raise ValueError('package aliases require explicit resolution support')
        version = entry.get('version')
        if not isinstance(version, str) or not VERSION.fullmatch(version):
            raise ValueError('exact resolved version required')
        integrity(entry.get('integrity'))
        registry_tarball(entry.get('resolved'), name, version)
        rows.append({'path': path, 'name': name, 'version': version,
                     'integrity': entry['integrity'], 'resolved': entry['resolved'],
                     'has_install_script': bool(entry.get('hasInstallScript'))})
    return {'manifest_sha256': hashlib.sha256(manifest_raw).hexdigest(),
            'lock_sha256': hashlib.sha256(lock_raw).hexdigest(),
            'packages': sorted(rows, key=lambda row: row['path'])}


def validate_tarball(data, entry):
    if len(data) > MAX_TARBALL or hashlib.sha512(data).digest() != integrity(entry['integrity']):
        raise ValueError('tarball size or integrity mismatch')
    total = 0
    names = set()
    package = None
    files = {}
    # Bound the entire stream, including PAX headers/padding, before tar parsing.
    with gzip.GzipFile(fileobj=io.BytesIO(data)) as compressed:
        expanded = compressed.read(MAX_UNPACKED + 1)
    if len(expanded) > MAX_UNPACKED:
        raise ValueError('expanded tar stream exceeds limit')
    with tarfile.open(fileobj=io.BytesIO(expanded), mode='r:') as archive:
        for member in archive:
            name = member.name
            path = PurePosixPath(name)
            if (str(path) != name.rstrip('/') or path.is_absolute() or '\\' in name
                    or any(p in ('.', '..') for p in path.parts)
                    or not path.parts or path.parts[0] != 'package'
                    or name.rstrip('/') in names):
                raise ValueError('unsafe or duplicate tar member')
            names.add(name.rstrip('/'))
            if len(names) > MAX_FILES or not (member.isfile() or member.isdir()):
                raise ValueError('unsupported tar member')
            if member.isdir():
                continue
            if len(path.parts) < 2 or member.size < 0:
                raise ValueError('invalid package file')
            total += member.size
            if total > MAX_UNPACKED:
                raise ValueError('expanded package too large')
            stream = archive.extractfile(member)
            digest = hashlib.sha256()
            content = bytearray() if name == 'package/package.json' else None
            while chunk := stream.read(1024 * 1024):
                digest.update(chunk)
                if content is not None:
                    content.extend(chunk)
                    if len(content) > 8 * 1024**2:
                        raise ValueError('oversize package metadata')
            files[str(path.relative_to('package'))] = digest.hexdigest()
            if content is not None:
                package = read_json(content)
    if not isinstance(package, dict) or package.get('name') != entry['name'] or package.get('version') != entry['version']:
        raise ValueError('tarball package identity mismatch')
    scripts = package.get('scripts', {})
    if not isinstance(scripts, dict):
        raise ValueError('invalid package scripts')
    hooks = {key: scripts[key] for key in ('preinstall', 'install', 'postinstall', 'prepare') if key in scripts}
    # node-gyp has an implicit install hook even with no scripts entry.
    return {'files': files, 'unpacked_bytes': total, 'lifecycle_scripts': hooks,
            'implicit_node_gyp': 'binding.gyp' in files,
            'tarball_sha256': hashlib.sha256(data).hexdigest()}
