"""A real dashboard process in a controlling PTY, parsed as a terminal screen.

No keystroke/model mocks: requests hit the real server; attach hits real tmux.
"""
import codecs
import fcntl
import json
import os
import pty
import select
import signal
import struct
import subprocess
import termios
import time
import urllib.request

import pyte
import pytest
from conftest import _binary
from test_terminal_workspace import real_terminal


class Dashboard:
    def __init__(self,t,args=(),outer_tmux=False):
        self.master,self.slave=pty.openpty()
        self.original=termios.tcgetattr(self.slave)
        fcntl.ioctl(self.slave,termios.TIOCSWINSZ,struct.pack('HHHH',35,120,0,0))
        self.screen=pyte.Screen(120,35);self.stream=pyte.Stream(self.screen)
        self.decoder=codecs.getincrementaldecoder('utf-8')('replace')
        def controlling_terminal():
            os.setsid();fcntl.ioctl(0,termios.TIOCSCTTY,0)
        command=[_binary(),*args]
        if outer_tmux:
            # The fixture's tmux server is created before this dashboard
            # process and therefore cannot inherit LECTERN_API from its
            # environment. Put the explicit hosted route in the command run
            # inside the outer tmux session so the new local-by-default CLI
            # cannot accidentally open a separate local database.
            command=["tmux","new-session","-s","dashboard-outer","env",
                     f"LECTERN_API={t['url']}",*command]
        self.proc=subprocess.Popen(command,stdin=self.slave,stdout=self.slave,stderr=self.slave,
          env={**t['env'],**({'XDG_CONFIG_HOME':str(t['root'].parent/'.console-config')} if 'root' in t else {}),'LECTERN_API':t['url'],'TERM':'xterm-256color','LECTERN_ATTACH_HOST':''},
          preexec_fn=controlling_terminal)
    def pump(self,duration=.1):
        end=time.monotonic()+duration
        while time.monotonic()<end:
            if select.select([self.master],[],[],.03)[0]:
                try:data=os.read(self.master,65536)
                except OSError:return
                self.stream.feed(self.decoder.decode(data))
    @property
    def text(self):return '\n'.join(self.screen.display)
    def wait(self,text,timeout=12):
        end=time.monotonic()+timeout
        while time.monotonic()<end:
            self.pump()
            if text in self.text:return
        raise AssertionError(f'Missing {text!r}:\n{self.text}')
    def wait_gone(self,text,timeout=12):
        end=time.monotonic()+timeout
        while time.monotonic()<end:
            self.pump()
            if text not in self.text:return
        raise AssertionError(f'Still showing {text!r}:\n{self.text}')
    def send(self,data):os.write(self.master,data.encode());self.pump(.15)
    def resize(self,cols,rows):
        self.screen.resize(rows,cols)
        fcntl.ioctl(self.slave,termios.TIOCSWINSZ,struct.pack('HHHH',rows,cols,0,0))
        os.kill(self.proc.pid,signal.SIGWINCH);self.pump(.3)
    def quit(self):
        self.send('q');self.proc.wait(timeout=10)
        assert self.proc.returncode==0
        assert termios.tcgetattr(self.slave)==self.original
    def close(self):
        if self.proc.poll() is None:self.proc.terminate();self.proc.wait(timeout=10)
        os.close(self.master);os.close(self.slave)


def test_dashboard_search_rename_live_refresh_and_resize(real_terminal):
    t=real_terminal;d=Dashboard(t)
    try:
        d.wait('Real terminal');d.wait('LIVE')
        d.send('/no-matches');d.wait('No matching items')
        d.send('\x01\x0bReal terminal\r');d.wait('Real terminal')
        d.send('e');d.wait('Rename')
        d.send('\x01\x0bRenamed in dashboard\x13')
        d.wait('Rename completed')
        assert t['api'](f"/sessions/{t['id']}")['name']=='Renamed in dashboard'
        d.send('\x1b');d.pump(.3)
        # A new server-side record appears automatically; no manual refresh.
        subprocess.run(['tmux','new-session','-d','-s','second-dashboard-agent','bash --norc'],env=t['env'],check=True)
        t['api']('/sessions/adopt',{'target_id':t['target_id'],'tmux_session':'second-dashboard-agent','name':'Arrived while open','agent':'claude','workdir':str(t['root'])})
        d.wait('Arrived while open',timeout=8)
        for cols,rows in [(80,24),(45,16),(140,40)]:
            d.resize(cols,rows);d.wait('Lectern');d.wait('q quit')
        d.send('?');d.wait('Keyboard shortcuts');d.send('?')
        d.quit()
        subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)
    finally:d.close()


def test_dashboard_details_are_readable_at_wide_and_narrow_widths_and_api_stays_json(real_terminal):
    t = real_terminal
    project = t['api']('/projects', {'name': 'Readable fixture project', 'target_id': t['target_id'],
                                     'repo_path': str(t['root'])})
    d = Dashboard(t)
    try:
        d.wait('Real terminal'); d.send('4'); d.wait('Readable fixture project')
        for cols in (144, 80):
            d.resize(cols, 30); d.send('/Readable fixture project\r'); d.send('\t'); d.wait('Repo Path')
            assert 'Repo Path' in d.text and 'repo_path' not in d.text
            assert 'Env Json' not in d.text
            assert '{' not in d.text and '"' not in d.text
            d.send('\x1b')
        raw = subprocess.run([_binary(), 'api', 'GET', '/projects'],
                             env={**t['env'], 'LECTERN_API': t['url'], 'TERM': 'dumb'},
                             text=True, capture_output=True, check=True)
        decoded = json.loads(raw.stdout)
        decoded = next(row for row in decoded if row['id'] == project['id'])
        assert decoded['name'] == 'Readable fixture project' and 'repo_path' in decoded
        d.quit()
    finally: d.close()


@pytest.mark.parametrize("outer_tmux",[False,True])
def test_dashboard_native_attach_detach_returns_to_selection(real_terminal,outer_tmux):
    t=real_terminal;d=Dashboard(t,['console'],outer_tmux=outer_tmux)
    try:
        d.wait('Real terminal');d.send('\r')
        # The preview already contains a shell prompt. Wait for a real tmux
        # client before typing, especially while SSH is still connecting.
        deadline=time.monotonic()+12
        while time.monotonic()<deadline:
            d.pump()
            clients=subprocess.check_output(['tmux','list-clients','-t','terminal-test','-F','#{client_name}'],env=t['env'],text=True).strip()
            if clients:break
        assert clients, d.text
        d.wait('$')
        d.send('printf dashboard-native-proof > dashboard-proof.txt\r')
        end=time.monotonic()+5
        while time.monotonic()<end and not (t['root']/'dashboard-proof.txt').exists():d.pump()
        assert (t['root']/'dashboard-proof.txt').read_text()=='dashboard-native-proof'
        d.send('\x02d');d.wait('Detached. Session keeps running.');d.wait('Real terminal')
        d.send('\r');d.wait('$');d.send('\x02d');d.wait('Detached. Session keeps running.')
        d.quit()
    finally:d.close()


def test_dashboard_creates_task_with_named_project_and_multiline_prompt(real_terminal):
    t=real_terminal
    project=t['api']('/projects',{'name':'Dashboard project','target_id':t['target_id'],'repo_path':str(t['root'])})
    d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('2n');d.wait('New task')
        d.send('Task from keyboard\t');d.wait('Dashboard project')
        d.send('\tFirst line\rSecond line')
        d.send('\t\t\t\t');d.wait('Dispatch now')
        d.send('\x1b[D\x13');d.wait('Create task completed')
        tasks=t['api']('/tasks');task=next(r for r in tasks if r['title']=='Task from keyboard')
        assert task['project_id']==project['id']
        assert task['prompt']=='First line\nSecond line'
        assert task['status']=='backlog'
        d.quit()
    finally:d.close()


def test_dashboard_project_picker_searches_many_projects_and_creates_selected(real_terminal):
    t=real_terminal
    projects=[]
    for i in range(1,101):
        projects.append(t['api']('/projects',{'name':f'Picker project {i:03d}',
            'target_id':t['target_id'],'repo_path':str(t['root'])}))
    chosen=next(p for p in projects if p['name']=='Picker project 099')
    # Keep the launched session local and deterministic while exercising the
    # real form, PTY, HTTP API, and tmux lifecycle.
    request=urllib.request.Request(t['url']+'/api/agents',method='PUT',
        headers={'Content-Type':'application/json'},
        data=json.dumps([{'name':'codex','command':'sleep 600'}]).encode())
    with urllib.request.urlopen(request) as response:
        assert response.status==200
    d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('n');d.wait('New session')
        d.send('Picker session\t\t')
        d.wait('Project')
        d.send('Picker project 099');d.wait('1 matches')
        # Selecting a project derives both the target and directory. The
        # next visible field is the agent; there is no stale target override
        # to tab through.
        d.send('\r');d.wait('Agent (without a profile)')
        # Return to the project with Shift-Tab, then move forward again. The
        # selected ID must survive the focus round trip before submit.
        d.send('\x1b[Z');d.wait('Selected: Picker project 099')
        d.send('\t\t');d.send('\x13');d.wait('Create session completed')
        row=next(s for s in t['api']('/sessions') if s['name']=='Picker session')
        assert row['project_id']==chosen['id'],row
    finally:d.close()


def test_dashboard_project_selection_uses_project_target_and_keeps_blank_target_path(real_terminal):
    t=real_terminal
    other=t['api']('/targets',{'name':'project-B-target','kind':'local'})
    project_a=t['api']('/projects',{'name':'Project A location','target_id':t['target_id'],'repo_path':str(t['root'])})
    project_b=t['api']('/projects',{'name':'Project B location','target_id':other['id'],'repo_path':str(t['root'])})
    request=urllib.request.Request(t['url']+'/api/agents',method='PUT',
        headers={'Content-Type':'application/json'},
        data=json.dumps([{'name':'codex','command':'sleep 600'}]).encode())
    with urllib.request.urlopen(request) as response:
        assert response.status==200
    d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('n');d.wait('New session')
        d.send('Project B session\t\t')
        d.wait('Project')
        d.send('Project B location');d.wait('1 matches');d.send('\r')
        # Enter skips the now-derived target and directory. Shift-Tab must
        # return to the project, proving hidden fields are absent from focus.
        d.wait('Agent (without a profile)');d.send('\x1b[Z');d.wait('Selected: Project B location')
        d.send('\t\x13');d.wait('Create session completed')
        row=next(s for s in t['api']('/sessions') if s['name']=='Project B session')
        assert row['project_id']==project_b['id'],row
        assert row['target_id']==other['id'],row
        assert row['workdir']==str(t['root']),row

        # The project-free path still exposes a target and directory in the
        # same form; verify it can be cancelled after reaching that field.
        d.send('n');d.wait('New session');d.send('Scratch session\t\t')
        d.wait('Project');d.send('\r');d.wait('Target');d.send('\x1b');
        d.wait('Real terminal');d.quit()
    finally:d.close()


def test_dashboard_context_upload_preserves_local_bytes(real_terminal,tmp_path):
    t=real_terminal;pdf=tmp_path/'context.pdf';pdf.write_bytes(b'PDF\x00\xffcontext')
    d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('u');d.wait('Local file path')
        d.send(str(pdf)+'\x13');d.wait('Uploaded:')
        assert any(p.read_bytes()==pdf.read_bytes() for p in t['root'].rglob('*.pdf'))
        d.quit()
    finally:d.close()


def test_dashboard_group_tree_search_refresh_and_attach(real_terminal):
    t=real_terminal
    request=urllib.request.Request(t['url']+f"/api/sessions/{t['id']}", method='PATCH',
        headers={'Content-Type':'application/json'},data=json.dumps({'group_path':'Work/Backend'}).encode())
    with urllib.request.urlopen(request) as response:assert response.status==200
    d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('ggg');d.wait('group: named group')
        # Collapsing from the selected session moves to its group header.
        d.send('[');d.wait('▸ Backend (1)')
        assert 'Real terminal' not in d.text
        d.send('[');d.wait('▸ Work (1)')
        d.pump(3.5);d.wait('▸ Work (1)')
        d.send('/Real terminal\r');d.wait('Real terminal')
        # Searching through a folded group still selects an actual session.
        d.send('\r')
        deadline=time.monotonic()+12
        clients=''
        while time.monotonic()<deadline:
            d.pump()
            clients=subprocess.check_output(['tmux','list-clients','-t','terminal-test','-F','#{client_name}'],env=t['env'],text=True).strip()
            if clients:break
        assert clients,d.text
        d.send('\x02d');d.wait('Detached. Session keeps running.')
        d.send('\x1b');d.wait('▸ Work (1)')
        # Expand with Enter; a header must never try to attach as a session.
        d.send('\r');d.wait('▸ Backend (1)')
        d.send('j\r');d.wait('Real terminal')
        d.quit()
        subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)
    finally:d.close()


def test_dashboard_edits_notification_settings_without_json(real_terminal):
    t=real_terminal;d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('7');d.wait('Notification settings')
        d.send('\t\tharmless-test-topic\x13');d.wait('Save settings completed')
        assert t['api']('/settings')['ntfy_topic']=='harmless-test-topic'
        d.quit()
    finally:d.close()


def test_dashboard_restores_group_layout_after_restart(real_terminal):
    t=real_terminal
    request=urllib.request.Request(t['url']+f"/api/sessions/{t['id']}", method='PATCH',
        headers={'Content-Type':'application/json'},data=json.dumps({'group_path':'Work/Backend'}).encode())
    with urllib.request.urlopen(request) as response:assert response.status==200
    d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('ggg');d.wait('group: named group')
        d.send('[');d.wait('▸ Backend (1)')
        d.send('2gg');d.send('1');d.wait('group: named group')
        d.quit()
    finally:d.close()
    d=Dashboard(t)
    try:
        d.wait('group: named group');d.wait('▸ Backend (1)')
        assert 'Real terminal' not in d.text
        d.send('/Real terminal\r');d.wait('Real terminal')
        d.send('\x1b');d.wait('▸ Backend (1)')
        d.quit()
        subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)
    finally:d.close()


@pytest.mark.parametrize("outer_tmux", [False, True])
def test_dashboard_mouse_click_attaches_and_returns(real_terminal, outer_tmux):
    t = real_terminal
    d = Dashboard(t, outer_tmux=outer_tmux)
    try:
        d.wait('Real terminal'); d.wait('LIVE')
        for width in (120, 80):
            d.resize(width, 35); d.pump(.3)
            y = next(i for i, line in enumerate(d.screen.display) if i >= 4 and 'Real terminal' in line)
            x = d.screen.display[y].index('Real terminal') + 1
            # Real SGR mouse down/up, not a mocked dashboard callback.
            d.send(f'\x1b[<0;{x};{y+1}M\x1b[<0;{x};{y+1}m')
            deadline = time.monotonic() + 12
            clients = ''
            while time.monotonic() < deadline:
                d.pump()
                clients = subprocess.check_output(['tmux', 'list-clients', '-t', 'terminal-test', '-F', '#{client_name}'], env=t['env'], text=True).strip()
                if clients:
                    break
            assert clients, d.text
            d.wait('$')
            d.send("printf mouse-attached > mouse-proof.txt\r")
            deadline = time.monotonic() + 4
            while time.monotonic() < deadline and not (t['root'] / 'mouse-proof.txt').exists():
                d.pump(.1)
            assert (t['root'] / 'mouse-proof.txt').read_text() == 'mouse-attached'
            d.send('\x02d'); d.wait('Detached. Session keeps running.'); d.wait('Real terminal')
        d.quit()
    finally:
        d.close()
