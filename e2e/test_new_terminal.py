"""One press from the Terminals view to a blank shell, on real ttyd and tmux."""
import subprocess
import time
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal


def test_new_terminal_button_opens_a_blank_shell(page,real_terminal):
    t=real_terminal;errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
    before={s['id'] for s in t['api']('/sessions')}
    page.goto(t['url']+'/#terminals')
    # With nothing open, the way in is offered right where the terminals will be.
    expect(page.locator('.terminal-empty')).to_be_visible()
    # One machine configured: there is nothing to choose between, so no menu.
    expect(page.locator('.terminal-new-machines')).to_have_count(0)
    page.locator('.terminal-empty').get_by_role('button',name='New terminal').click()
    expect(page.get_by_role('tab')).to_have_count(1,timeout=15000)
    created=[s for s in t['api']('/sessions') if s['id'] not in before]
    assert len(created)==1,created
    shell=created[0]
    f=page.frame_locator(f'iframe[src="/terminal/session/{shell["id"]}?embed=1"]')
    expect(f.locator('#connection')).to_have_text('Connected',timeout=20000)
    # It is a shell you can type into, not an agent waiting on a prompt.
    f.locator('#agent-terminal').click()
    page.keyboard.type('echo BLANK-SHELL-$((6*7))');page.keyboard.press('Enter')
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('BLANK-SHELL-42',timeout=10000)
    out=subprocess.check_output(['tmux','capture-pane','-p','-t','='+shell['tmux_session']+':'],env=t['env']).decode()
    assert 'BLANK-SHELL-42' in out
    # With a terminal open the button moves to the tab bar, and a second press
    # opens a second shell beside the first rather than reusing it.
    page.locator('.terminal-tabbar').get_by_role('button',name='New terminal').click()
    expect(page.get_by_role('tab')).to_have_count(2,timeout=15000)
    assert len(t['api']('/sessions'))==len(before)+2
    assert not errors,errors


@pytest.mark.parametrize("viewport",[{"width":390,"height":844},{"width":1440,"height":900}])
def test_new_terminal_picker_opens_a_tracked_shell_in_the_project_folder(page,real_terminal,viewport):
    """The caret picker opens ONE new tracked shell in the chosen project's own
    folder — not the scratch room, not the preexisting shared project terminal."""
    t=real_terminal;page.set_viewport_size(viewport)
    project=t['api']('/projects',{'name':'Picker project','target_id':t['target_id'],
                                  'repo_path':str(t['root']),'default_agent':'claude'})
    before={s['id'] for s in t['api']('/sessions')}
    page.goto(t['url']+'/#terminals')
    bar=page.locator('.terminal-tabbar')
    expect(bar.locator('.terminal-new-machines')).to_have_count(1)
    bar.locator('.terminal-new-machines > summary').click()
    panel=bar.locator('.terminal-new-machines > .terminal-new-picker')
    expect(panel).to_be_visible()
    # The picker fits the viewport and spells out the project's folder.
    box=panel.bounding_box()
    assert box and box['x']>=-1 and box['x']+box['width']<=viewport['width']+1,box
    row=panel.get_by_role('menuitem').filter(has_text='Picker project')
    expect(row).to_contain_text(str(t['root']))
    # It is searchable, so a long project list is easy to narrow.
    panel.get_by_label('Search projects and machines').fill('Picker project')
    expect(panel.get_by_role('menuitem')).to_have_count(1)
    row.click()
    created=[]
    deadline=time.monotonic()+10
    while time.monotonic()<deadline and not created:
        created=[s for s in t['api']('/sessions') if s['id'] not in before]
        if not created: time.sleep(0.1)
    assert len(created)==1,created
    shell=created[0]
    assert shell['project_id']==project['id'],shell
    assert shell['agent']=='shell' and shell['model'] in ('',None),shell
    assert shell['workdir']==str(t['root']),shell
    assert 'lectern-scratch' not in (shell['workdir'] or '')
    # It attaches immediately into a real private tmux shell with progress.identity.
    expect(page.get_by_role('tab')).to_have_count(1,timeout=15000)
    f=page.frame_locator(f'iframe[src="/terminal/session/{shell["id"]}?embed=1"]')
    expect(f.locator('#connection')).to_have_text('Connected',timeout=20000)
    f.locator('#agent-terminal').click()
    # The pane wraps long paths, so prove the working directory by asking the
    # shell to write it where the test can read it whole.
    page.keyboard.type('echo PROJECT-SHELL-$((6*7)); pwd > project-shell-pwd.txt');page.keyboard.press('Enter')
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('PROJECT-SHELL-42',timeout=10000)
    deadline=time.monotonic()+10
    pwd_file=t['root']/'project-shell-pwd.txt'
    while time.monotonic()<deadline and not pwd_file.exists():
        page.wait_for_timeout(100)
    assert pwd_file.read_text().strip()==str(t['root']),pwd_file.read_text()
    # The main button is unchanged: a plain scratch shell on the default machine.
    page.locator('.terminal-tabbar').get_by_role('button',name='New terminal').click()
    expect(page.get_by_role('tab')).to_have_count(2,timeout=15000)
    blank=[]
    deadline=time.monotonic()+10
    while time.monotonic()<deadline and not blank:
        blank=[s for s in t['api']('/sessions') if s['id'] not in before and s['id']!=shell['id']]
        if not blank: time.sleep(0.1)
    assert len(blank)==1 and blank[0]['project_id'] in (None,0),blank
    assert 'lectern-scratch' in (blank[0]['workdir'] or ''),blank


def test_project_picker_stays_inside_keyboard_viewport_with_many_projects(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({'width': 390, 'height': 844})
    page.add_init_script('''
      const view = new EventTarget();
      Object.assign(view, {height:844, width:390, offsetTop:0, offsetLeft:0, scale:1});
      Object.defineProperty(window, 'visualViewport', {value:view, configurable:true});
      window.resizeVisible = () => { view.height=400; view.offsetTop=35; view.dispatchEvent(new Event('resize')); };
    ''')
    page.route('**/api/projects', lambda route: route.fulfill(json=[
        {'id': i+100, 'name': f'Project {i}', 'repo_path': '/a/very/long/project/folder/' + 'x'*100,
         'target_id': t['target_id'], 'target_name': 'Project machine'} for i in range(85)
    ]))
    page.goto(t['url'] + '/#terminals')
    # Exercise both the toolbar and the lower empty-state entry point.
    for entry in ['.terminal-tabbar', '.terminal-empty']:
        picker = page.locator(entry + ' .terminal-new-machines')
        picker.locator('summary').click()
        panel = picker.locator('.terminal-new-picker')
        expect(panel).to_be_visible()
        page.evaluate('resizeVisible()')
        expect(panel).to_have_css('position', 'fixed')
        page.wait_for_function('''() => {
          const b=document.querySelector('.terminal-new-machines[open] .terminal-new-picker').getBoundingClientRect();
          return b.x>=0 && b.right<=390 && b.y>=35 && b.bottom<=435;
        }''')
        panel.get_by_label('Search projects and machines').fill('Project 84')
        expect(panel.locator('.terminal-new-project')).to_have_count(1)
        expect(panel.locator('.terminal-new-project')).to_be_in_viewport()
        page.keyboard.press('Escape')
        expect(panel).not_to_be_visible()
