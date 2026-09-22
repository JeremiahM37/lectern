import json,urllib.request,uuid,hashlib
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_command_palette import search
from test_terminal_tabs import frame,ready


def open_search(page):
    search(page,'search saved conversations').get_by_role('option').click()
    dialog=page.get_by_role('dialog',name='Search saved conversations',exact=True)
    expect(dialog).to_be_visible()
    return dialog


def prepare(t):
    home=t['root']/'search-home';folder=home/'sessions';folder.mkdir(parents=True)
    cid=str(uuid.uuid4());path=folder/(cid+'.jsonl')
    rows=[{'type':'session_meta','payload':{'id':cid,'cwd':str(t['root']/'untracked')}}]
    for text,channel in [('old résumé needle <script>window.injected=true</script>','final'),('PRIVATE_SENTINEL','analysis')]+[('ordinary filler '*1000,'final')]*200+[('Latest saved message sentinel','final')]:
        rows.append({'type':'response_item','payload':{'type':'message','role':'assistant','channel':channel,'content':[{'type':'output_text','text':text}]}})
    path.write_text(''.join(json.dumps(row)+'\n' for row in rows))
    req=urllib.request.Request(t['url']+'/api/agents',method='PUT',data=json.dumps([{'name':'codex','command':'codex','env':{'CODEX_HOME':str(home),'LECTERN_NATIVE_SEARCH_CACHE':str(t['root']/'cache')}}]).encode(),headers={'Content-Type':'application/json'})
    urllib.request.urlopen(req).close()
    return path


@pytest.mark.parametrize('width',[390,1440])
def test_native_search_reads_old_match_and_retains_terminal(page,real_terminal,width):
    t=real_terminal;path=prepare(t);original=hashlib.sha256(path.read_bytes()).hexdigest();errors=[]
    page.on('pageerror',lambda e:errors.append(str(e)))
    page.set_viewport_size({'width':width,'height':844});page.goto(t['url'])
    search(page,'real terminal').get_by_role('option').click();one=frame(page,t['id']);ready(one)
    one.locator('body').evaluate('()=>window.savedSearchIdentity="retained"')
    d=open_search(page);d.get_by_label('Agent',exact=True).select_option('codex')
    d.get_by_label('Target',exact=True).select_option(str(t['target_id']))
    d.get_by_label('Conversation text').fill('résumé needle');d.get_by_role('button',name='Search',exact=True).click()
    expect(d.locator('.ns-result')).to_have_count(1,timeout=20000)
    expect(d.locator('.ns-status')).to_contain_text('1 conversation found')
    page.screenshot(path=f'/tmp/lectern-native-search-results-{width}.png')
    assert d.evaluate('(el)=>el.scrollWidth<=el.clientWidth')
    d.get_by_label('Conversation text').focus();page.keyboard.press('ArrowDown');expect(d.locator('.ns-result')).to_be_focused();page.keyboard.press('Enter')
    expect(d.locator('.ns-match')).to_contain_text('old résumé needle')
    assert 'PRIVATE_SENTINEL' not in d.inner_text() and page.evaluate('window.injected') is None
    expect(d.get_by_role('button',name='Earlier messages')).to_be_disabled()
    d.get_by_role('button',name='Later messages').click()
    expect(d.locator('.ns-read-status')).to_contain_text('Saved messages')
    expect(d.locator('.ns-match')).to_have_count(0)
    d.get_by_role('button',name='Earlier messages').click()
    expect(d.locator('.ns-match')).to_contain_text('old résumé needle')
    d.get_by_role('button',name='Latest indexed',exact=True).click()
    expect(d.locator('.nh-messages')).to_contain_text('Latest saved message sentinel')
    expect(d.get_by_role('button',name='Later messages')).to_be_disabled()
    d.get_by_role('button',name='Back to match').click()
    expect(d.locator('.ns-match')).to_contain_text('old résumé needle')
    assert d.evaluate('(el)=>el.scrollWidth<=el.clientWidth')
    if width==390:
        page.set_viewport_size({'width':390,'height':400})
        expect(d).to_have_css('height','400px')
        assert d.get_by_role('button',name='Back to results').is_visible()
        page.set_viewport_size({'width':390,'height':844})
    page.screenshot(path=f'/tmp/lectern-native-search-{width}.png')
    d.get_by_role('button',name='Back to results').click()
    assert original==hashlib.sha256(path.read_bytes()).hexdigest()
    path.write_text(path.read_text().replace('old r','new r'))
    edited=hashlib.sha256(path.read_bytes()).hexdigest()
    d.locator('.ns-result').click();expect(d.locator('.ns-read-status')).to_contain_text('changed')
    d.get_by_role('button',name='Back to results').click();d.locator('.ns-advanced summary').click()
    d.get_by_role('button',name='Rebuild and search').click();expect(d.locator('.ns-result')).to_contain_text('new résumé needle',timeout=15000)
    d.get_by_role('button',name='Close saved conversation search').click()
    assert one.locator('body').evaluate('()=>window.savedSearchIdentity')=='retained'
    assert page.locator('#terminal-workspace iframe').count()==1 and not errors
    assert edited==hashlib.sha256(path.read_bytes()).hexdigest()


def test_native_search_progress_retry_cancel_and_close(page,real_terminal):
    t=real_terminal;page.goto(t['url']);calls=[];expired=[False]
    result={'id':'test-job','query':'needle','done':False,'complete':False,'results':[], 'scopes':[{'id':'1','target':'remote','agent':'codex','state':'indexing','progress':{'documents':3,'pending_files':7,'issues':[],'oversized_entries':0}}]}
    def route(r):
        calls.append(r.request.method)
        if r.request.method=='POST':r.fulfill(json=result)
        elif r.request.method=='DELETE':
            if expired[0]:expired[0]=False;r.fulfill(status=404,json={'detail':'search expired'})
            else:r.fulfill(json={**result,'done':True})
        else:r.fulfill(status=503,json={'detail':'temporarily offline'})
    page.route('**/api/conversation-search**',route)
    d=open_search(page);d.get_by_label('Conversation text').fill('needle');d.get_by_role('button',name='Search',exact=True).click()
    expect(d.locator('.ns-status')).to_contain_text('Could not update search')
    expect(d.get_by_role('button',name='Retry connection')).to_be_visible()
    d.locator('.ns-progress summary').click();expect(d.locator('.ns-progress')).to_contain_text('7 pending')
    d.get_by_role('button',name='Retry connection').click()
    expired[0]=True
    with page.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/api/conversation-search')):
        d.get_by_role('button',name='Search',exact=True).click()
    d.get_by_role('button',name='Stop search').click()
    d.get_by_role('button',name='Close saved conversation search').click()
    page.wait_for_timeout(200)
    assert 'DELETE' in calls


def test_native_search_direct_entries_and_close_during_start(page,real_terminal):
    from test_terminal_workspace import open_terminal,terminal_tool
    t=real_terminal;page.goto(t['url']+'/#sessions')
    page.locator('#sess-saved-search').click()
    d=page.get_by_role('dialog',name='Search saved conversations',exact=True)
    expect(d).to_be_visible();d.get_by_role('button',name='Close saved conversation search').click()
    open_terminal(page,t)
    page.evaluate('''()=>{
      const original=window.fetch;
      window.searchCanceled=[];
      window.fetch=(url,opts={})=>{
        if(url==='/api/conversation-search'&&opts.method==='POST')return new Promise(resolve=>{window.releaseSearch=()=>resolve(new Response(JSON.stringify({id:'late-job',done:false,complete:false,results:[],scopes:[]})))});
        if(url==='/api/conversation-search/late-job'&&opts.method==='DELETE'){window.searchCanceled.push(url);return Promise.resolve(new Response('{}'));}
        return original(url,opts);
      };
    }''')
    terminal_tool(page,'#search-conversations');expect(d).to_be_visible()
    d.get_by_label('Conversation text').fill('needle');d.get_by_role('button',name='Search',exact=True).click()
    page.wait_for_function('!!window.releaseSearch')
    d.get_by_role('button',name='Close saved conversation search').click()
    page.evaluate('window.releaseSearch()')
    page.wait_for_function('window.searchCanceled.length===1')
    expect(d).to_have_count(0)
    expect(page.locator('#connection')).to_have_text('Connected')
