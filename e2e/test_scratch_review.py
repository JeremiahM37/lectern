"""The scratch sweep against a real filesystem: what it removes, and what it asks about."""
import os
import re
import subprocess
import time

import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal

OLD=time.time()-30*86400


def workspace(root,name,files=(),old=True):
    d=root/name;d.mkdir();subprocess.run(['git','init','-q',str(d)],check=True)
    for f in files:(d/f).write_text('work')
    if old:
        for p in [d,*d.iterdir()]:os.utime(p,(OLD,OLD))
    return d


@pytest.mark.parametrize('real_terminal',[{'isolated_scratch':True}],indirect=True)
def test_sweep_removes_idle_empties_and_asks_about_work(page,real_terminal):
    t=real_terminal;errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
    root=t['root'].parent/'scratch';assert t['env']['LECTERN_SCRATCH_ROOT']==str(root)
    workspace(root,'shell-empty-old')
    workspace(root,'shell-empty-new',old=False)
    workspace(root,'codex-with-files',files=['notes.md'])
    # The case that is easy to get wrong: nothing on disk, but a conversation
    # happened here. Claude files its history under a slug of the directory.
    talked=workspace(root,'shell-talked-in')
    history=t['root'].parent/'claude-home'/'projects'/re.sub(r'[^A-Za-z0-9]','-',str(talked))
    history.mkdir(parents=True);(history/'c.jsonl').write_text('{"role":"user"}\n'*50)
    verdicts={e['name']:e['verdict'] for e in t['api']('/scratch')['targets'][0]['entries']}
    assert verdicts=={'shell-empty-old':'empty','shell-empty-new':'recent','codex-with-files':'work','shell-talked-in':'work'},verdicts
    # Reporting changes nothing.
    assert sorted(p.name for p in root.iterdir())==sorted(verdicts)
    page.goto(t['url']+'/#sessions')
    review=page.locator('#scratch-review');review.locator('summary').click()
    expect(page.locator('#scratch-summary')).to_contain_text('2 hold work that no project claims',timeout=15000)
    expect(review.locator('.scratch-row')).to_have_count(2)
    expect(review.locator('[data-scratch="shell-talked-in"]')).to_contain_text('Claude conversation')
    page.locator('#scratch-sweep').click()
    expect(page.locator('#scratch-sweep')).to_have_count(0,timeout=15000)
    left=sorted(p.name for p in root.iterdir())
    assert left==['.trash','codex-with-files','shell-empty-new','shell-talked-in'],left
    # Removed means recoverable: the directory is whole inside the trash.
    trashed=list((root/'.trash').iterdir())
    assert len(trashed)==1 and trashed[0].name.startswith('shell-empty-old.lec-trashed-') and (trashed[0]/'.git').is_dir(),trashed
    # A person settles the rest: keep one for good, discard the other.
    review.locator('[data-scratch="shell-talked-in"]').get_by_role('button',name='Keep').click()
    expect(review.locator('.scratch-row')).to_have_count(1,timeout=15000)
    assert (talked/'.lectern-keep').exists()
    page.once('dialog',lambda d:d.accept())
    review.locator('[data-scratch="codex-with-files"]').get_by_role('button',name='Discard').click()
    expect(review.locator('.scratch-row')).to_have_count(0,timeout=15000)
    assert (root/'codex-with-files').exists() is False
    assert any(p.name.startswith('codex-with-files.lec-trashed-') and (p/'notes.md').read_text()=='work' for p in (root/'.trash').iterdir())
    assert not errors,errors


@pytest.mark.parametrize('real_terminal',[{'isolated_scratch':True}],indirect=True)
def test_a_running_session_cannot_be_discarded(real_terminal):
    t=real_terminal
    # The fixture's own session runs in its workspace; put one inside the root.
    root=t['root'].parent/'scratch';d=workspace(root,'shell-in-use',files=['a.txt'])
    subprocess.run(['tmux','new-session','-d','-s','in-use','-c',str(d),'bash','--norc'],env=t['env'],check=True)
    t['api']('/sessions/adopt',{'target_id':t['target_id'],'tmux_session':'in-use','workdir':str(d),'name':'In use','agent':'claude'})
    entry=t['api']('/scratch')['targets'][0]['entries'][0]
    assert entry['verdict']=='live',entry
    import json,urllib.request,urllib.error
    req=urllib.request.Request(t['url']+'/api/scratch/discard',data=json.dumps({'target_id':t['target_id'],'name':'shell-in-use'}).encode(),headers={'Content-Type':'application/json'})
    with pytest.raises(urllib.error.HTTPError) as refused:urllib.request.urlopen(req)
    assert refused.value.code==409 and (d/'a.txt').exists()
