import hashlib,json,subprocess,urllib.request,time
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_terminal_dashboard import Dashboard
from test_terminal_tabs import frame,ready
from test_native_search import prepare,open_search
from test_command_palette import search


def prepare_fork(t):
    source=prepare(t);cwd=t['root']/'untracked';cwd.mkdir()
    subprocess.run(['git','init','-q',str(cwd)],check=True)
    (cwd/'base.txt').write_text('base\n')
    subprocess.run(['git','-C',str(cwd),'add','.'],check=True)
    subprocess.run(['git','-C',str(cwd),'-c','user.name=Test','-c','user.email=test@example.invalid','commit','-qm','fixture'],check=True)
    proof=t['root']/'global-fork.json';stub=t['root']/'fork-agent'
    stub.write_text('#!/usr/bin/env python3\nimport os,sys,json,time\nfrom pathlib import Path\nPath(os.environ["FORK_PROOF"]).write_text(json.dumps({"argv":sys.argv[1:],"cwd":os.getcwd()}))\nprint("GLOBAL FORK READY",flush=True)\nwhile True:time.sleep(1)\n');stub.chmod(0o755)
    req=urllib.request.Request(t['url']+'/api/agents',method='PUT',data=json.dumps([{'name':'codex','command':str(stub),'env':{'CODEX_HOME':str(t['root']/'search-home'),'LECTERN_NATIVE_SEARCH_CACHE':str(t['root']/'cache'),'FORK_PROOF':str(proof)},'fork_args':['fork','{id}']}]).encode(),headers={'Content-Type':'application/json'})
    urllib.request.urlopen(req).close()
    return source,cwd,proof


@pytest.mark.parametrize('width,isolated',[(1440,False),(390,True)])
def test_web_forks_global_search_and_retains_original_terminal(page,real_terminal,width,isolated):
    t=real_terminal;source,cwd,proof=prepare_fork(t);original=hashlib.sha256(source.read_bytes()).hexdigest();errors=[]
    page.on('pageerror',lambda e:errors.append(str(e)));page.set_viewport_size({'width':width,'height':844});page.goto(t['url'])
    search(page,'real terminal').get_by_role('option').click();one=frame(page,t['id']);ready(one);one.locator('body').evaluate('()=>window.originalForkTerminal=true')
    d=open_search(page);d.get_by_label('Agent',exact=True).select_option('codex');d.get_by_label('Conversation text').fill('needle');d.get_by_role('button',name='Search',exact=True).click()
    expect(d.locator('.ns-result')).to_have_count(1,timeout=15000);d.locator('.ns-result').click()
    d.get_by_role('button',name='Fork conversation',exact=True).click()
    expect(d.get_by_label('Launch settings',exact=True)).to_contain_text('Current agent settings')
    d.get_by_label('Session name',exact=True).fill('Global fork proof')
    if isolated:
        d.get_by_label('Fork workspace').select_option('isolated');d.get_by_label('Branch (blank = automatic)').fill('web-global-fork')
    expect(d.locator('.ns-fork-warning')).to_contain_text('including messages after the match')
    assert d.evaluate('(el)=>el.scrollWidth<=el.clientWidth')
    page.screenshot(path=f'/tmp/lectern-global-fork-{width}.png')
    page.evaluate('''()=>{const original=window.fetch;window.fetch=(url,opts)=>String(url).endsWith('/fork')?new Promise(resolve=>setTimeout(()=>resolve(original(url,opts)),500)):original(url,opts);}''')
    with page.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/fork')) as response:
        d.get_by_role('button',name='Create fork',exact=True).click()
        expect(d.get_by_role('button',name='Close saved conversation search')).to_be_disabled()
        page.keyboard.press('Escape');expect(d).to_be_visible()
    created=response.value.json();assert response.value.status==(202 if isolated else 201),created
    expect(d).to_have_count(0)
    if isolated:
        page.locator('.scard',has_text='Global fork proof').get_by_role('button',name='⌨ Attach',exact=True).click(timeout=20000)
    child=frame(page,created['id']);expect(child.owner).to_be_visible(timeout=15000);expect(child.locator('#connection')).to_have_text('Connected',timeout=15000)
    expect(child.locator('#agent-terminal .xterm-screen')).to_contain_text('GLOBAL FORK READY',timeout=15000)
    assert page.locator('#terminal-workspace iframe').count()==2
    assert one.locator('body').evaluate('()=>window.originalForkTerminal') is True
    observed=json.loads(proof.read_text());assert isolated==(observed['cwd']!=str(cwd))
    cid=json.loads(source.read_text().splitlines()[0])['payload']['id'];assert cid in observed['argv']
    assert hashlib.sha256(source.read_bytes()).hexdigest()==original and not errors


def test_terminal_forks_search_result_without_placeholder_session(real_terminal):
    t=real_terminal;source,cwd,proof=prepare_fork(t);original=hashlib.sha256(source.read_bytes()).hexdigest()
    d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('F');d.wait('Search saved conversations')
        d.send('needle\t\t\x1b[C\x1b[C\x13');d.wait('old résumé needle');d.send('\r');d.wait('MATCHING MESSAGE')
        d.send('f');d.wait('Fork whole saved conversation');d.wait('Current agent settings')
        d.send('\t\x01\x0bTerminal global fork\t\x1b[C\x13')
        d.wait('Fork workspace setup started.',timeout=20);d.wait('Terminal global fork')
        deadline=time.monotonic()+10
        while not proof.exists() and time.monotonic()<deadline:time.sleep(.05)
        observed=json.loads(proof.read_text());assert observed['cwd']!=str(cwd)
        assert json.loads(source.read_text().splitlines()[0])['payload']['id'] in observed['argv']
        assert hashlib.sha256(source.read_bytes()).hexdigest()==original
        assert len(t['api']('/sessions?include_ended=1'))==2
        # Wait for the dashboard's refresh as well as the target-side proof:
        # starting the agent precedes publishing the ready reservation.
        deadline=time.monotonic()+12
        while 'setting up' in d.text.lower() and time.monotonic()<deadline:d.pump()
        assert 'setting up' not in d.text.lower(),d.text
        d.send('\r');d.wait('GLOBAL FORK READY');d.send('\x02d');d.wait('Detached. Session keeps running.')
        d.quit()
    finally:d.close()
