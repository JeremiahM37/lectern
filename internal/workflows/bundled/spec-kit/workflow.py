#!/usr/bin/env python3
"""Prepare support files without replacing user files; render one pinned command."""
import argparse
import os
from pathlib import Path
import re
import stat
import sys

MODES = ("constitution", "specify", "clarify", "plan", "tasks", "analyze", "checklist", "implement", "converge")
UPSTREAM = Path(__file__).resolve().parent / "upstream"


def install_missing(root, relative, data, mode=0o644):
    """Walk by directory descriptors: no project symlinks can redirect writes."""
    parts = Path(relative).parts
    fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        for part in parts[:-1]:
            try:
                os.mkdir(part, 0o755, dir_fd=fd)
            except FileExistsError:
                pass
            next_fd = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = next_fd
        try:
            out = os.open(parts[-1], os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode, dir_fd=fd)
        except FileExistsError:
            existing = os.stat(parts[-1], dir_fd=fd, follow_symlinks=False)
            if not stat.S_ISREG(existing.st_mode):
                raise ValueError(f"refusing non-regular support path: {relative}")
            return False
        with os.fdopen(out, "wb") as stream:
            stream.write(data)
        return True
    finally:
        os.close(fd)


def prepare(root):
    created = 0
    for directory, suffix, executable in (("templates", ".md", False), ("scripts/bash", ".sh", True)):
        for source in sorted((UPSTREAM / directory).glob("*" + suffix)):
            relative = ".specify/" + source.relative_to(UPSTREAM).as_posix()
            created += install_missing(root, relative, source.read_bytes(), 0o755 if executable else 0o644)
    for filename in ("LICENSE", "PROVENANCE.json"):
        created += install_missing(root, ".specify/lectern-spec-kit/" + filename, (UPSTREAM / filename).read_bytes())
    return created


def render(mode):
    raw = (UPSTREAM / "templates/commands" / (mode + ".md")).read_text()
    header, body = raw.removeprefix("---\n").split("\n---\n", 1)
    command = re.search(r"^\s+sh:\s*(.+)$", header, re.MULTILINE)
    if "{SCRIPT}" in body:
        if not command:
            raise ValueError("upstream command has no bash script mapping")
        body = body.replace("{SCRIPT}", "bash .specify/" + command.group(1).strip())
    body = re.sub(r"__SPECKIT_COMMAND_([A-Z]+)__", lambda m: "lectern-spec-kit " + m.group(1).lower(), body)
    body = body.replace("$ARGUMENTS", "the user's request following the selected mode in this conversation")
    body = body.replace("{ARGS}", "the user's request following the selected mode in this conversation")
    body = re.sub(r"(?<![\w./])/memory/constitution\.md", ".specify/memory/constitution.md", body)
    body = body.replace("`templates/checklist-template.md`", "`.specify/templates/checklist-template.md`")
    body = body.replace("`specify preset resolve spec-template`", "`bash .specify/scripts/bash/resolve-template.sh spec-template --json`")
    preface = ("Adapter rules: follow the parent SKILL.md. Existing user/project instructions take precedence. "
               "Extension hooks are not installed or executed by this adapter; report configured hooks instead. "
               "Preserve the active Lectern worktree and branch, project instructions and shared memory.\n\n")
    return "# Lectern Spec Kit: " + mode + "\n\n" + preface + body.strip() + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=MODES)
    parser.add_argument("--project", type=Path, default=Path.cwd())
    args = parser.parse_args()
    root = args.project.absolute()
    if root.is_symlink() or not root.is_dir():
        parser.error("project must be an existing real directory")
    try:
        instruction = render(args.mode)
        created = prepare(root)
    except (OSError, ValueError) as exc:
        print(f"Spec Kit preparation failed: {exc}", file=sys.stderr)
        return 1
    print(f"Prepared {created} missing support files; existing project files preserved.", file=sys.stderr)
    print(instruction)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
