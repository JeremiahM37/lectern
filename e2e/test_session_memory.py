"""A session and the memory store sharing one key, seen from the browser."""
import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest
from playwright.sync_api import expect
from conftest import _start, _unused_port


class Store(BaseHTTPRequestHandler):
    asked=[]
    def log_message(self,*a):pass
    def do_GET(self):
        path=self.path.split('?')[0]
        if path=='/api/memory/changes':
            Store.asked.append(self.path)
            body={'since':'2026-01-01T00:00:00Z','counts':{'learned':1,'changed':1},'changes':[
                {'kind':'learned','at':'x','text':'Staging listens on 5433','path':'memory/demo.md','agent':'claude-code','topic':'demo'},
                {'kind':'changed','at':'x','text':'Deploys use the release script','replaced_text':'Deploys are manual','agent':'claude-code','path':'memory/demo.md','topic':'demo'}]}
        elif path=='/api/notes':body=[{'path':'memory/unrelated.md'}]
        elif path=='/api/health':body={'ok':True}
        else:body={'results':[],'memories':[]}
        raw=json.dumps(body).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length') or 0))
        self.send_response(201);self.send_header('Content-Length','2');self.end_headers();self.wfile.write(b'{}')


@pytest.fixture()
def with_store():
    Store.asked=[]
    store=HTTPServer(('127.0.0.1',0),Store);threading.Thread(target=store.serve_forever,daemon=True).start()
    port=_unused_port()
    proc=_start(port,{'AGENTDECK_GRIMOIRE_URL':f'http://127.0.0.1:{store.server_port}'})
    try:yield f'http://127.0.0.1:{port}'
    finally:
        proc.terminate();proc.wait(timeout=15);store.shutdown()


def test_a_session_card_shows_what_its_agent_remembered(page,with_store):
    url=with_store;errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
    page.goto(url+'/#sessions')
    created=page.evaluate("""async()=>{const p=(await (await fetch('/api/projects')).json())[0];
        const s=await (await fetch('/api/sessions',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({project_id:p.id,name:'Remembers things'})})).json();return s.id}""")
    card=page.locator('.scard',has_text='Remembers things')
    expect(card).to_be_visible(timeout=15000)
    # Nothing is asked of the store until someone looks.
    assert Store.asked==[]
    card.locator('.session-memory summary').click()
    memory=card.locator('.session-memory')
    expect(memory).to_contain_text('Staging listens on 5433',timeout=10000)
    # A correction shows what it replaced, not only what stands.
    expect(memory).to_contain_text('was: Deploys are manual')
    expect(memory).to_contain_text(f'agentdeck-s{created}')
    assert f'session=agentdeck-s{created}' in Store.asked[-1],Store.asked
    # The seeded project's managed note is not in the store, and that is said
    # rather than left to look like a project with no memory.
    expect(memory.locator('.session-memory-link')).to_contain_text('Project memory unlinked')
    assert not errors,errors
