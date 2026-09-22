"""Small, real SSH server used by the remote browser acceptance fixture.

This process is started by the test inside the reviewed test namespace.  It
only accepts the fixture's generated public key and executes commands as the
same unprivileged test user.  The PTY path is intentionally implemented here:
Lectern's SSH terminal attachment uses ``ssh -tt`` and therefore exercises
the same interactive transport as a real remote target.
"""

import argparse
import asyncio
import json
import os
import pty
import signal
import struct
import termios
from pathlib import Path

import asyncssh


class FixtureServer(asyncssh.SSHServer):
    def __init__(self, public_key: bytes):
        self._public_key = public_key

    def begin_auth(self, username: str) -> bool:
        # Always require authentication. Returning False for an unexpected
        # username would make AsyncSSH treat that username as unauthenticated.
        return True

    def public_key_auth_supported(self) -> bool:
        return True

    def validate_public_key(self, username: str, key) -> bool:
        return username == "fixture" and key.public_data == self._public_key


def _set_winsize(fd: int, width: int, height: int) -> None:
    if width <= 0 or height <= 0:
        return
    try:
        import fcntl

        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", height, width, 0, 0))
    except OSError:
        pass


async def _waitpid(pid: int) -> int:
    loop = asyncio.get_running_loop()
    _, status = await loop.run_in_executor(None, os.waitpid, pid, 0)
    return os.waitstatus_to_exitcode(status)


async def _read_pty(fd: int, process) -> None:
    loop = asyncio.get_running_loop()
    while True:
        try:
            data = await loop.run_in_executor(None, os.read, fd, 32768)
        except (OSError, asyncio.CancelledError):
            return
        if not data:
            return
        process.stdout.write(data)


async def _write_pty(fd: int, process) -> None:
    loop = asyncio.get_running_loop()
    while True:
        try:
            data = await process.stdin.read(32768)
        except asyncssh.TerminalSizeChanged as exc:
            # AsyncSSH delivers each window-change request through the stdin
            # stream. Keep forwarding input after applying the new size.
            _set_winsize(fd, exc.width or 80, exc.height or 24)
            continue
        except (OSError, asyncio.CancelledError):
            return
        if not data:
            return
        try:
            await loop.run_in_executor(None, os.write, fd, data)
        except (OSError, asyncio.CancelledError):
            return


async def _resize_pty(fd: int, process) -> None:
    previous = None
    try:
        while True:
            width, height, _, _ = process.term_size
            width = width or 80
            height = height or 24
            size = (width, height)
            if size != previous:
                _set_winsize(fd, width, height)
                previous = size
            await asyncio.sleep(0.1)
    except (Exception, asyncio.CancelledError):
        return


async def _run_pty(process, command: str) -> None:
    master, child = pty.openpty()
    width, height, _, _ = process.term_size
    _set_winsize(master, width or 80, height or 24)

    pid = os.fork()
    if pid == 0:
        try:
            os.close(master)
            os.setsid()
            # The slave opened by openpty becomes the controlling terminal for
            # the child shell after TIOCSCTTY.  This is what tmux requires.
            import fcntl

            fcntl.ioctl(child, termios.TIOCSCTTY, 0)
            os.dup2(child, 0)
            os.dup2(child, 1)
            os.dup2(child, 2)
            if child > 2:
                os.close(child)
            os.environ["TERM"] = process.term_type or "xterm-256color"
            os.execl("/bin/bash", "bash", "--noprofile", "--norc", "-c", command)
        except BaseException:
            os._exit(127)

    os.close(child)
    reader = asyncio.create_task(_read_pty(master, process))
    writer = asyncio.create_task(_write_pty(master, process))
    resizer = asyncio.create_task(_resize_pty(master, process))
    try:
        status = await _waitpid(pid)
        process.exit(status)
    finally:
        reader.cancel()
        writer.cancel()
        resizer.cancel()
        os.close(master)
        await asyncio.gather(reader, writer, resizer, return_exceptions=True)


async def _copy_stream(reader, writer) -> None:
    try:
        while True:
            data = await reader.read(32768)
            if not data:
                return
            writer.write(data)
    except (asyncio.CancelledError, OSError):
        return


async def _copy_stdin(reader, child_stdin) -> None:
    try:
        while True:
            data = await reader.read(32768)
            if not data:
                return
            child_stdin.write(data)
            await child_stdin.drain()
    except (asyncio.CancelledError, OSError):
        return
    finally:
        child_stdin.close()


async def _run_exec(process, command: str) -> None:
    child = await asyncio.create_subprocess_exec(
        "/bin/bash", "--noprofile", "--norc", "-c", command,
        stdin=asyncio.subprocess.PIPE,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    assert child.stdin is not None
    assert child.stdout is not None
    assert child.stderr is not None
    stdin_task = asyncio.create_task(_copy_stdin(process.stdin, child.stdin))
    output_tasks = [
        asyncio.create_task(_copy_stream(child.stdout, process.stdout)),
        asyncio.create_task(_copy_stream(child.stderr, process.stderr)),
    ]
    try:
        status = await child.wait()
        # Let both output streams reach EOF before closing the SSH channel. A
        # command can exit before its final buffered writes have been relayed.
        await asyncio.gather(*output_tasks, return_exceptions=True)
        process.exit(status)
    finally:
        stdin_task.cancel()
        await asyncio.gather(stdin_task, return_exceptions=True)
        if child.returncode is None:
            child.kill()


async def handle_process(process) -> None:
    command = process.command or "/bin/bash --noprofile --norc -i"
    if process.term_type:
        await _run_pty(process, command)
    else:
        await _run_exec(process, command)


async def main(args) -> None:
    client_key = asyncssh.read_private_key(args.key_path)
    server_key = asyncssh.generate_private_key("ssh-ed25519")
    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    loop.add_signal_handler(signal.SIGTERM, stop.set)
    loop.add_signal_handler(signal.SIGINT, stop.set)
    server = await asyncssh.create_server(
        lambda: FixtureServer(client_key.public_data),
        "127.0.0.1", 0,
        server_host_keys=[server_key],
        process_factory=handle_process,
        encoding=None,
        allow_pty=True,
    )
    ready = {"port": server.get_port(), "user": "fixture"}
    Path(args.ready_path).write_text(json.dumps(ready) + "\n")
    await stop.wait()
    server.close()
    await server.wait_closed()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--key-path", required=True)
    parser.add_argument("--ready-path", required=True)
    asyncio.run(main(parser.parse_args()))
