import hashlib,json,os,re,subprocess,time,urllib.request,uuid
from pathlib import Path
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal,open_terminal,terminal_tool
from test_terminal_dashboard import Dashboard


def prepare(t,agent='claude'):
    home=t['root']/'native-home';home.mkdir()
    cid=str(uuid.uuid4());other=str(uuid.uuid4())
    if agent=='claude':
        folder=home/'projects'/re.sub(r'[^a-zA-Z0-9]','-',str(t['root']))
        env={'CLAUDE_CONFIG_DIR':str(home)};args=['--resume','{id}','--fork-session']
    else:
        folder=home/'sessions';env={'CODEX_HOME':str(home)};args=['fork','{id}']
        subprocess.run(['tmux','new-session','-d','-s','native-source','bash --norc'],env=t['env'],check=True)
        source=t['api']('/sessions/adopt',{'target_id':t['target_id'],'tmux_session':'native-source','workdir':str(t['root']),'name':'Native source','agent':'codex'})
        t['id']=source['id']
    folder.mkdir(parents=True)
    for ident,cwd in [(cid,str(t['root'])),(other,'/another-workspace')]:
        records=[]
        if agent=='codex':records.append({'type':'session_meta','payload':{'id':ident,'cwd':cwd}})
        for i in range(230):
            role='user' if i%2==0 else 'assistant';text=f'History proof {i:03d} <script>window.injected=true</script>'
            if agent=='codex':records.append({'type':'response_item','payload':{'type':'message','role':role,'content':[{'type':'input_text' if role=='user' else 'output_text','text':text}]}})
            else:records.append({'type':role,'sessionId':ident,'cwd':cwd,'message':{'role':role,'content':text}})
        (folder/(ident+'.jsonl')).write_text(''.join(json.dumps(r)+'\n' for r in records))
    stub=t['root']/'agent.py';stub.write_text('import sys,json,time\nfrom pathlib import Path\nPath("fork-argv.json").write_text(json.dumps(sys.argv[1:]))\nprint("FORK READY",flush=True)\nwhile True:time.sleep(1)\n')
    req=urllib.request.Request(t['url']+'/api/agents',method='PUT',data=json.dumps([{'name':agent,'command':'python3 '+str(stub),'env':env,'fork_args':args,'resume_id_args':['--resume','{id}'] if agent=='claude' else ['resume','{id}']}]).encode(),headers={'Content-Type':'application/json'})
    urllib.request.urlopen(req).close()
    return cid,folder/(cid+'.jsonl'),other


@pytest.mark.parametrize('agent',['claude','codex'])
def test_saved_conversation_reader_and_native_fork(page,real_terminal,agent):
    t=real_terminal;cid,file,other=prepare(t,agent);original=hashlib.sha256(file.read_bytes()).hexdigest()
    req=urllib.request.Request(t['url']+f'/api/sessions/{t["id"]}',method='PATCH',data=json.dumps({'group_path':'Work/Forks'}).encode(),headers={'Content-Type':'application/json'});urllib.request.urlopen(req).close()
    page.set_viewport_size({'width':390,'height':844});open_terminal(page,t);terminal_tool(page,'#saved-conversations')
    dialog=page.get_by_role('dialog',name='Saved conversations');expect(dialog).to_be_visible()
    expect(dialog.locator('.nh-fork')).to_be_disabled()
    expect(dialog.locator('.nh-select option')).to_have_count(2)
    dialog.locator('.nh-select').select_option(cid)
    expect(dialog.locator('.nh-messages')).to_contain_text('History proof 229')
    assert dialog.locator('script').count()==0 and page.evaluate('window.injected') is None
    dialog.get_by_role('button',name='Load earlier messages').click()
    expect(dialog.locator('.nh-messages')).to_contain_text('History proof 000')
    expect(dialog.locator('.nh-messages')).to_contain_text('History proof 229')
    assert dialog.locator('.nh-messages article').count()==230
    dialog.locator('.nh-select').select_option('')
    expect(dialog.locator('.nh-fork')).to_be_disabled()
    expect(dialog.locator('.nh-messages')).to_be_empty()
    dialog.locator('.nh-select').select_option(cid)
    expect(dialog.locator('.nh-messages')).to_contain_text('History proof 229')
    assert dialog.evaluate('(el)=>el.scrollWidth<=el.clientWidth+1')
    page.screenshot(path=f'/tmp/lectern-native-history-{agent}.png')
    dialog.get_by_role('button',name='Fork conversation',exact=True).click()
    dialog.locator('.nh-fork-name').fill('Forked history proof')
    dialog.get_by_role('button',name='Create fork',exact=True).click()
    expect(dialog).not_to_be_visible(timeout=15000)
    deadline=time.monotonic()+10
    while time.monotonic()<deadline and not (t['root']/'fork-argv.json').exists():time.sleep(.1)
    argv=json.loads((t['root']/'fork-argv.json').read_text())
    assert cid in argv and '--last' not in argv and '--continue' not in argv
    assert ('--fork-session' in argv) if agent=='claude' else argv[0]=='fork'
    assert any(s['name']=='Forked history proof' and s['group_path']=='Work/Forks' for s in t['api']('/sessions'))
    assert hashlib.sha256(file.read_bytes()).hexdigest()==original
    expect(page.locator('#connection')).to_have_text('Connected')


def test_tui_reads_saved_conversation_and_confirms_fork(real_terminal):
    t=real_terminal;cid,file,other=prepare(t);d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('H');d.wait('Saved conversations')
        d.send('\x13');d.wait('History proof 030');d.send('O');d.wait('History proof 000')
        d.send('H');d.wait('Saved conversations');d.send('\t\x1b[C\x13')
        d.wait('Both agents share');d.send('n');assert not (t['root']/'fork-argv.json').exists()
        d.send('H');d.wait('Saved conversations');d.send('\t\x1b[C\x13');d.wait('Both agents share');d.send('y')
        d.wait('Fork conversation completed');d.quit()
        assert cid in json.loads((t['root']/'fork-argv.json').read_text())
    finally:d.close()
