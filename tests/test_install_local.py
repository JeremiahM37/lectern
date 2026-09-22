"""Installer checks that never modify the host installation or user config."""

import os
from pathlib import Path
import shutil
import subprocess


ROOT = Path(__file__).resolve().parents[1]
INSTALLER = ROOT / "tools" / "install-local.sh"


def run_installer(tmp_path, *args, extra_path=()):
    home = tmp_path / "home"
    home.mkdir(exist_ok=True)
    path = [str(p) for p in extra_path]
    path.extend(p for p in (shutil.which("git"), shutil.which("tmux"), "/usr/bin", "/bin") if p)
    env = {
        **os.environ,
        "HOME": str(home),
        "PATH": os.pathsep.join(path),
        "XDG_BIN_HOME": str(home / ".local/bin"),
        "XDG_STATE_HOME": str(home / ".state"),
    }
    return subprocess.run(
        ["bash", str(INSTALLER), *map(str, args)],
        env=env,
        text=True,
        capture_output=True,
        check=False,
        cwd=tmp_path,
    ), home


def fake_binary(path):
    path.write_text("#!/bin/sh\ncase \"$1\" in version) echo local-test-1;; local) exit 0;; esac\n")
    path.chmod(0o755)


def test_binary_install_does_not_clobber_remote_client(tmp_path):
    binary = tmp_path / "lectern"
    fake_binary(binary)
    prefix = tmp_path / "bin"
    remote = prefix / "lectern"
    prefix.mkdir()
    remote.write_text("#!/bin/sh\n# remote launcher\n")
    remote.chmod(0o755)

    result, home = run_installer(tmp_path, "--binary", binary, "--prefix", prefix)

    assert result.returncode == 0, result.stderr
    assert (prefix / "lectern-local").is_file()
    assert remote.read_text() == "#!/bin/sh\n# remote launcher\n"
    assert "lectern-local local" in result.stdout
    assert home.exists()


def test_reinstall_updates_an_existing_local_command(tmp_path):
    first = tmp_path / "first-lectern"
    second = tmp_path / "second-lectern"
    fake_binary(first)
    second.write_text("#!/bin/sh\ncase \"$1\" in version) echo local-test-2;; local) exit 0;; esac\n")
    second.chmod(0o755)
    prefix = tmp_path / "bin"

    result, _ = run_installer(tmp_path, "--binary", first, "--prefix", prefix)
    assert result.returncode == 0, result.stderr
    result, home = run_installer(tmp_path, "--binary", second, "--prefix", prefix)

    assert result.returncode == 0, result.stderr
    assert (prefix / "lectern").is_file()
    assert not (prefix / "lectern-local").exists()
    assert subprocess.run([prefix / "lectern", "version"], text=True, capture_output=True, check=True).stdout.strip() == "local-test-2"
    assert list((home / ".state/lectern/local/backups").rglob("lectern"))


def test_external_replacement_invalidates_managed_marker(tmp_path):
    binary = tmp_path / "lectern"
    fake_binary(binary)
    prefix = tmp_path / "bin"

    result, _ = run_installer(tmp_path, "--binary", binary, "--prefix", prefix)
    assert result.returncode == 0, result.stderr
    remote = prefix / "lectern"
    remote.write_text("#!/bin/sh\necho external remote\n")
    remote.chmod(0o755)

    result, _ = run_installer(tmp_path, "--binary", binary, "--prefix", prefix)

    assert result.returncode == 0, result.stderr
    assert (prefix / "lectern-local").is_file()
    assert remote.read_text() == "#!/bin/sh\necho external remote\n"


def test_source_build_works_when_called_outside_checkout(tmp_path):
    fake_go = tmp_path / "go"
    fake_go.write_text(
        "#!/bin/sh\n"
        "while [ $# -gt 0 ]; do [ \"$1\" = -o ] && { out=$2; shift 2; continue; }; shift; done\n"
        "printf '#!/bin/sh\\ncase \"$1\" in version) echo source-test-1;; local) exit 0;; esac\\n' > \"$out\"\n"
        "chmod +x \"$out\"\n"
    )
    fake_go.chmod(0o755)
    source = ROOT
    prefix = tmp_path / "bin"

    result, _ = run_installer(tmp_path, "--source", source, "--prefix", prefix, extra_path=(tmp_path,))

    assert result.returncode == 0, result.stderr
    installed = prefix / "lectern"
    assert installed.is_file() and os.access(installed, os.X_OK)
    assert subprocess.run([installed, "version"], text=True, capture_output=True, check=True).stdout.strip() == "source-test-1"


def test_windows_shell_is_directed_to_wsl(tmp_path):
    fake_uname = tmp_path / "uname"
    fake_uname.write_text("#!/bin/sh\nprintf 'MINGW64_NT\\n'\n")
    fake_uname.chmod(0o755)

    # Exercise the Windows guard without requiring a Windows machine.
    result = subprocess.run(
        ["bash", str(INSTALLER), "--binary", tmp_path / "missing"],
        env={**os.environ, "HOME": str(tmp_path / "home"), "PATH": os.pathsep.join((str(tmp_path), "/usr/bin", "/bin"))},
        text=True,
        capture_output=True,
        check=False,
    )
    assert result.returncode == 1
    assert "WSL2" in result.stderr
