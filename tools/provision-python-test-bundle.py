#!/usr/bin/python3
"""Stage a curated, RECORD-verified offline pytest runtime; never activate it.

Checksums attest installed RECORD contents, not registry/signature provenance.
The source must be a trusted installed environment. No package code is imported.
Key = sha256(json.dumps(manifest_without_key, sort_keys=True,
                       separators=(',', ':')).encode()).hexdigest().
--stage permits unprivileged test destinations; production requires root and a
root-owned destination parent. Activation is a separate controller operation.
"""
from __future__ import annotations

import argparse
import base64
import csv
import hashlib
from importlib.metadata import PathDistribution
import io
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import sys
import tempfile

PACKAGES = {
    "pytest": {"pytest", "_pytest", "py.py"},
    "iniconfig": {"iniconfig"},
    "packaging": {"packaging"},
    "pluggy": {"pluggy"},
    "pygments": {"pygments"},
}
ENTRYPOINTS = {
    "pytest": {"../../../bin/pytest", "../../../bin/py.test"},
    "pygments": {"../../../bin/pygmentize"},
}
DEFAULT_SOURCE = Path("/home/admin/.venvs/verify/lib/python3.13/site-packages")
DEFAULT_DESTINATION = Path("/mnt/bulk/lectern-autonomy/dependencies/python")


class BundleError(ValueError):
    pass


def canonical(data):
    return json.dumps(data, sort_keys=True, separators=(",", ":")).encode()


def no_links(path):
    path = Path(os.path.abspath(path))
    for item in (path, *path.parents):
        if item.is_symlink():
            raise BundleError(f"symlink path: {item}")
    return path


def read_regular(path):
    no_links(path)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise BundleError(f"not an independent regular file: {path}")
        if info.st_size > 16 * 1024 * 1024:
            raise BundleError(f"file exceeds limit: {path}")
        content = stream.read(16 * 1024 * 1024 + 1)
        if len(content) != info.st_size:
            raise BundleError(f"file changed while reading: {path}")
        return content


def safe_relative(raw):
    path = PurePosixPath(raw)
    if not raw or "\\" in raw or path.is_absolute() or str(path) != raw or any(p in {".", ".."} for p in path.parts):
        raise BundleError(f"invalid RECORD path: {raw}")
    return path


def cache_path(path):
    return "__pycache__" in path.parts and path.suffix == ".pyc"


def collect(source):
    source = no_links(source)
    files, versions = {}, {}
    total_bytes = 0
    for package, modules in PACKAGES.items():
        matches = list(source.glob(package + "-*.dist-info"))
        if len(matches) != 1:
            raise BundleError(f"expected exactly one {package} distribution")
        dist_dir = no_links(matches[0])
        if not dist_dir.is_dir():
            raise BundleError(f"not a distribution directory: {dist_dir}")
        # Read metadata as data; never import the installed module.
        metadata_bytes = read_regular(dist_dir / "METADATA")
        dist = PathDistribution(dist_dir)
        version = dist.version
        if dist.metadata["Name"].lower() != package or not version:
            raise BundleError(f"distribution identity mismatch: {package}")
        if dist_dir.name != f"{package}-{version}.dist-info":
            raise BundleError(f"distribution directory/version mismatch: {package}")
        versions[package] = version
        record_name = dist_dir.name + "/RECORD"
        record = read_regular(dist_dir / "RECORD")
        seen, included = set(), set()
        for row in csv.reader(io.StringIO(record.decode("utf-8"))):
            if len(row) != 3:
                raise BundleError("malformed RECORD row")
            raw, checksum, size = row
            if raw in seen:
                raise BundleError(f"duplicate RECORD path: {raw}")
            seen.add(raw)
            # Installed console scripts are deliberately not carried into the
            # worker. No other parent traversal is tolerated.
            if raw in ENTRYPOINTS.get(package, set()):
                continue
            relative = safe_relative(raw)
            if relative.parts[0] not in modules | {dist_dir.name, "__pycache__"}:
                raise BundleError(f"unexpected package file: {raw}")
            if cache_path(relative):
                continue
            if relative.parts[0] == "__pycache__":
                raise BundleError(f"unexpected cache file: {raw}")
            content = read_regular(source / raw)
            digest = hashlib.sha256(content).digest()
            verified = raw != record_name
            if verified:
                if not checksum.startswith("sha256=") or size != str(len(content)):
                    raise BundleError(f"missing/unsupported checksum or size: {raw}")
                expected = base64.urlsafe_b64encode(digest).decode().rstrip("=")
                if checksum != "sha256=" + expected:
                    raise BundleError(f"RECORD checksum mismatch: {raw}")
            elif checksum or size:
                raise BundleError("RECORD must have its standard unhashed self entry")
            total_bytes += len(content)
            if len(files) >= 4096 or total_bytes > 64 * 1024 * 1024:
                raise BundleError("curated bundle exceeds size/file budget")
            if raw in files:
                raise BundleError(f"distribution file collision: {raw}")
            files[raw] = (content, {"path": raw, "sha256": digest.hex(), "size": len(content), "record_verified": verified})
            included.add(raw)
        if record_name not in included or dist_dir.name + "/METADATA" not in included:
            raise BundleError(f"missing distribution metadata/RECORD: {package}")
        if files[dist_dir.name + "/METADATA"][0] != metadata_bytes or files[record_name][0] != record:
            raise BundleError("metadata changed during collection")
        # Refuse unrecorded source inside the allowlisted trees, rather than
        # silently trusting an incomplete/tampered inventory. Cache is excluded.
        for root_name in modules | {dist_dir.name}:
            root = source / root_name
            if not root.exists():
                raise BundleError(f"missing module root: {root_name}")
            paths = [root] if root.is_file() else root.rglob("*")
            for item in paths:
                no_links(item)
                relative = item.relative_to(source)
                if item.is_dir():
                    continue
                if cache_path(relative):
                    continue
                if relative.as_posix() not in included:
                    raise BundleError(f"unrecorded file: {relative}")
    return versions, files


def validate_production_destination(destination):
    """Trust the dedicated dependency tree, not ownership of its mountpoint.

    The production layout is lectern-autonomy/dependencies/python. Its three
    directory levels are controller-owned; an admin-owned mount such as
    /mnt/bulk above that boundary is legitimate. Ancestor symlinks remain banned.
    """
    destination = no_links(destination)
    if os.geteuid() != 0:
        raise BundleError("production publishing requires root; use --stage for testing")
    for item in (destination, destination.parent, destination.parent.parent):
        try:
            info = item.stat()
        except FileNotFoundError:
            if item == destination:
                continue
            raise BundleError(f"missing production dependency directory: {item}")
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            raise BundleError(f"untrusted production destination: {item}")


def provision(source=DEFAULT_SOURCE, destination=DEFAULT_DESTINATION, *, stage=False):
    if sys.version_info[:3] != (3, 13, 5):
        raise BundleError("curated runtime requires Python 3.13.5")
    destination = no_links(destination)
    if not stage:
        validate_production_destination(destination)
    versions, files = collect(source)
    manifest = {"schema_version": 1, "kind": "python-test-runtime", "runtime_family": "python3.13", "python_version": "3.13.5", "checksum_verified": True, "packages": versions, "files": [files[name][1] for name in sorted(files)]}
    key = hashlib.sha256(canonical(manifest)).hexdigest()
    manifest["key"] = key
    destination.mkdir(parents=True, exist_ok=True)
    target = destination / key
    if target.exists():
        raise BundleError(f"bundle already exists; never replace immutable data: {target}")
    temporary = Path(tempfile.mkdtemp(prefix=".python-stage-", dir=destination))
    try:
        for name, (content, _) in files.items():
            item = temporary / "site-packages" / name
            item.parent.mkdir(parents=True, exist_ok=True)
            item.write_bytes(content)
            item.chmod(0o444)
        (temporary / "manifest.json").write_bytes(canonical(manifest) + b"\n")
        (temporary / "manifest.json").chmod(0o444)
        for directory in sorted((p for p in temporary.rglob("*") if p.is_dir()), key=lambda p: len(p.parts), reverse=True):
            directory.chmod(0o555)
        temporary.chmod(0o555)
        temporary.rename(target)
    except BaseException:
        if temporary.exists():
            for directory in (temporary, *(p for p in temporary.rglob("*") if p.is_dir())):
                directory.chmod(0o700)
            shutil.rmtree(temporary)
        raise
    return target, manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-site-packages", type=Path, default=DEFAULT_SOURCE)
    parser.add_argument("--destination-root", type=Path, default=DEFAULT_DESTINATION)
    parser.add_argument("--stage", action="store_true")
    args = parser.parse_args()
    try:
        target, manifest = provision(args.source_site_packages, args.destination_root, stage=args.stage)
    except (BundleError, OSError, UnicodeError) as exc:
        parser.exit(1, f"bundle refused: {exc}\n")
    print(json.dumps({"path": str(target), "key": manifest["key"], "checksum_verified": True}))


if __name__ == "__main__":
    main()
