"""Review actual staged/working files while the agent's tmux session stays alive."""
import subprocess
from pathlib import Path
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal, open_terminal, terminal_tool, type_command
from test_terminal_dashboard import Dashboard


def prepare(t):
    root=t['root']
    for key,value in [('user.name','Review Test'),('user.email','test@example.com')]:
        subprocess.run(['git','config',key,value],cwd=root,check=True)
    (root/'a.txt').write_text('original\n')
    subprocess.run(['git','add','.'],cwd=root,check=True)
    subprocess.run(['git','commit','-qm','base'],cwd=root,check=True)
    (root/'a.txt').write_text('staged proof\n')
    subprocess.run(['git','add','a.txt'],cwd=root,check=True)
    (root/'a.txt').write_text('working proof\n')
    (root/'b.txt').write_text('second file\n')
    (root/'<img src=x onerror=alert(1)>.txt').write_text('<script>window.reviewInjected=true</script>\n')


@pytest.mark.parametrize('width',[1280,390])
def test_terminal_review_opens_the_workspace_and_keeps_terminal_attached(page,real_terminal,width):
    t=real_terminal;prepare(t);page.set_viewport_size({'width':width,'height':820})
    open_terminal(page,t);terminal_tool(page,'#review')
    review=page.locator('#session-review');expect(review).to_be_visible()
    # The live diff against HEAD: staged and working changes together.
    expect(review.locator('.dfile',has_text='a.txt')).to_contain_text('+working proof',timeout=15000)
    expect(review.locator('.dfile',has_text='a.txt')).to_contain_text('-original')
    expect(review.locator('.dfile',has_text='b.txt')).to_contain_text('+second file')
    # A hostile file name and content are shown as text, never run.
    expect(review).to_contain_text('<script>window.reviewInjected=true</script>')
    assert page.evaluate('window.reviewInjected') is None
    assert review.locator('.dcode img,.dcode script').count()==0
    # Staged and working changes are separate on the Commit tab.
    review.get_by_role('tab',name='Commit').click()
    staged=review.locator('.git-section',has_text='Staged (1)')
    expect(staged).to_contain_text('a.txt',timeout=15000)
    staged.locator('.git-file-name',has_text='a.txt').click()
    expect(staged.locator('.dcode')).to_contain_text('+staged proof')
    review.get_by_role('tab',name='Changes').click()
    (t['root']/'b.txt').write_text('updated proof\n')
    review.get_by_role('button',name='Close').click()
    terminal_tool(page,'#review')
    review=page.locator('#session-review')
    expect(review.locator('.dfile',has_text='b.txt')).to_contain_text('+updated proof',timeout=15000)
    assert review.evaluate('(el)=>el.scrollWidth<=el.clientWidth+1')
    page.screenshot(path=f'/tmp/lectern-live-review-{width}.png')
    review.get_by_role('button',name='Close').click()
    # The app's styles leave with the workspace; the terminal keeps its own.
    assert page.locator('style[data-lectern-review]').count()==0
    expect(page.locator('#connection')).to_have_text('Connected')
    page.locator('#agent-terminal').click();type_command(page,'echo still-attached')
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('still-attached')


def test_session_card_opens_live_review(page,real_terminal):
    t=real_terminal;prepare(t);page.goto(t['url']+'/')
    page.locator('.tab[data-tab="sessions"]').click()
    card=page.locator('.sess-card').filter(has_text='Real terminal')
    # Resolve from the named action menu rather than an internal JS function.
    page.get_by_text('More ···',exact=True).first.click()
    page.get_by_role('button',name='Review changes',exact=True).click()
    expect(page.get_by_role('dialog',name='Review changes')).to_be_visible()
    expect(page.locator('.review-files')).to_contain_text('a.txt')


def test_terminal_dashboard_reviews_live_changes(real_terminal):
    t=real_terminal;prepare(t);d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('v');d.wait('Review changes');d.wait('working')
        d.send('s');d.wait('staged');d.wait('+staged proof')
        d.send('s');d.wait('working');d.send('\x1b[C');d.wait('+working proof')
        d.send('\x1b[C');d.wait('+second file')
        d.send('\x1b');d.wait('Real terminal');d.quit()
    finally:d.close()


def test_review_ignores_slow_previous_file_response(page,real_terminal):
    t=real_terminal;prepare(t)
    page.add_init_script('''const originalFetch=window.fetch;
      window.fetch=async (...args)=>{const response=await originalFetch(...args);
        if(String(args[0]).includes('/changes?') && String(args[0]).includes('path=b.txt'))
          await new Promise(resolve=>setTimeout(resolve,700));
        return response;};''')
    # The session card's "Review changes" keeps the read-only changes view.
    page.goto(t['url']+'/');page.locator('.tab[data-tab="sessions"]').click()
    page.get_by_text('More ···',exact=True).first.click()
    page.get_by_role('button',name='Review changes',exact=True).click()
    dialog=page.get_by_role('dialog',name='Review changes')
    dialog.get_by_role('button',name='?? b.txt',exact=True).click()
    dialog.get_by_role('button',name='M a.txt').click()
    expect(dialog.locator('.review-path')).to_have_text('a.txt')
    expect(dialog.locator('.review-patch')).to_contain_text('+working proof')
    page.wait_for_timeout(1000)
    expect(dialog.locator('.review-path')).to_have_text('a.txt')
    expect(dialog.locator('.review-patch')).not_to_contain_text('+second file')
