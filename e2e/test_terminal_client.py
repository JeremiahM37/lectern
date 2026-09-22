"""Real CLI process → HTTP server → store and tmux/file operations."""
import json
import os
from pathlib import Path
import subprocess
from conftest import _binary
from test_terminal_workspace import real_terminal


def command(t, *args, input=None, check=True):
    return subprocess.run([_binary(), *args], env={**t['env'], 'LECTERN_API':t['url']},
        input=input, text=True, capture_output=True, timeout=30, check=check)


def test_terminal_client_session_context_and_files(real_terminal, tmp_path):
    t = real_terminal
    sessions = json.loads(command(t, 'api', 'GET', '/sessions').stdout)
    assert sessions[0]['id'] == t['id']
    local = tmp_path / "Résumé's $(touch owned).pdf"
    local.write_bytes(b'PDF context\x00\xff')
    attachment = json.loads(command(t, 'upload', 'session', str(t['id']), str(local)).stdout)
    assert Path(attachment['path']).read_bytes() == local.read_bytes()
    assert not (t['root']/'owned').exists()
    files = json.loads(command(t, 'files', 'session', str(t['id'])).stdout)
    assert 'hello.txt' in json.dumps(files)
    saved = tmp_path/'download.txt'
    command(t, 'download', 'session', str(t['id']), 'hello.txt', str(saved))
    assert saved.read_bytes() == (t['root']/'hello.txt').read_bytes()
    result = command(t, 'download', 'session', str(t['id']), 'hello.txt', str(saved), check=False)
    assert result.returncode != 0
    command(t, 'api', 'PATCH', f"/sessions/{t['id']}", '-', input='{"name":"CLI renamed"}')
    assert t['api'](f"/sessions/{t['id']}")['name'] == 'CLI renamed'
    menu = command(t, 'console', input='1\nb\n8\nq\n')
    assert 'CLI renamed' in menu.stdout and 'Error:' not in menu.stdout


def test_terminal_client_routine_lifecycle(real_terminal):
    t = real_terminal
    project = json.loads(command(t, 'api', 'POST', '/projects', json.dumps({
        'name':'CLI project','target_id':t['api'](f"/sessions/{t['id']}")['target_id'],'repo_path':str(t['root'])})).stdout)
    routine = json.loads(command(t, 'api', 'POST', '/routines', json.dumps({
        'name':'CLI routine','project_ids':[project['id']],'prompt':'Review code', 'dispatch':False})).stdout)
    rid = routine['id']
    command(t, 'api', 'PATCH', f'/routines/{rid}', '{"enabled":false}')
    assert next(r for r in t['api']('/routines') if r['id']==rid)['enabled'] is False
    result = json.loads(command(t, 'api', 'POST', f'/routines/{rid}/run', '{}').stdout)
    assert result
    command(t, 'api', 'DELETE', f'/routines/{rid}')
    assert not any(r['id']==rid for r in t['api']('/routines'))
    bad = command(t, 'api', 'GET', '/tasks/99999', check=False)
    assert bad.returncode != 0 and bad.stderr and not bad.stdout


def test_linux_installer_and_default_console(real_terminal, tmp_path):
    t = real_terminal
    home = tmp_path/'client-home';home.mkdir()
    tools = tmp_path/'fake-ssh';tools.mkdir()
    ssh = tools/'ssh';ssh.write_text('#!/bin/sh\nuname -s\nuname -m\n');ssh.chmod(0o755)
    scp = tools/'scp';scp.write_text('#!/bin/sh\ncp "$TEST_LECTERN_BINARY" "$3"\n');scp.chmod(0o755)
    env = {**os.environ,'HOME':str(home),'PATH':str(tools)+':'+os.environ['PATH'],
        'TEST_LECTERN_BINARY':_binary()}
    from conftest import ROOT
    installer = ROOT/'web/static/desktop/install-lectern-cli.sh'
    for _ in range(2):
        subprocess.run(['bash',str(installer),'--server','test-server','--api',t['url']],env=env,check=True,capture_output=True)
    client = home/'.local/bin/lectern'
    result = subprocess.run([str(client)],env=env,input='1\nb\nq\n',text=True,capture_output=True,check=True,timeout=10)
    assert 'Real terminal' in result.stdout and 'Error:' not in result.stdout
    assert list((home/'.local/state/lectern').glob('cli-*/client'))
    result = subprocess.run([str(client),'api','GET','/sessions'],env=env,capture_output=True,text=True,check=True)
    assert json.loads(result.stdout)[0]['id']==t['id']


def test_menu_and_direct_attach_use_portable_term(real_terminal,tmp_path):
    """Real CLI and tmux PTY, with SSH transport replaced at the peer boundary."""
    import pty,select,time
    t=real_terminal
    tools=tmp_path/'transport';tools.mkdir()
    ssh=tools/'ssh'
    ssh.write_text('#!/bin/sh\nprintf "%s" "$TERM" > "$TERM_RECEIPT"\nexec tmux attach -t =terminal-test\n')
    ssh.chmod(0o755)
    receipt=tmp_path/'term.txt'
    env={**t['env'],'LECTERN_API':t['url'],'LECTERN_ATTACH_HOST':'test-peer',
      'PATH':str(tools)+':'+os.environ['PATH'],'TERM':'xterm-kitty','TERM_RECEIPT':str(receipt)}
    for menu in [False,True]:
        master,slave=pty.openpty()
        child=subprocess.Popen([_binary(),*(['console','--plain'] if menu else ['attach','session',str(t['id'])])],
          stdin=slave,stdout=slave,stderr=slave,env=env,start_new_session=True)
        os.close(slave)
        output=b''
        def until(token):
            nonlocal output
            deadline=time.time()+10
            while time.time()<deadline:
                if token in output: output=b'';return
                if select.select([master],[],[],.1)[0]:
                    try:output+=os.read(master,65536)
                    except OSError:break
            raise AssertionError(output.decode(errors='replace'))
        try:
            if menu:
                until(b'Open:');os.write(master,b'1\n')
                until(b'Choose:');os.write(master,(str(t['id'])+'\n').encode())
                until(b'Action:');os.write(master,b'attach\n')
            until(b'$')
            assert receipt.read_text()=='xterm-256color'
            proof='menu-proof' if menu else 'direct-proof'
            os.write(master,('printf '+proof+' > '+proof+'.txt\r').encode())
            deadline=time.time()+5
            while time.time()<deadline and not (t['root']/(proof+'.txt')).exists():time.sleep(.05)
            assert (t['root']/(proof+'.txt')).read_text()==proof
            os.write(master,b'\x02d')
            if menu:
                until(b'Action:');os.write(master,b'b\n')
                until(b'Choose:');os.write(master,b'b\n')
                until(b'Open:');os.write(master,b'q\n')
            child.wait(timeout=10)
            assert child.returncode==0
        finally:
            if child.poll() is None:child.terminate();child.wait(timeout=10)
            os.close(master)
