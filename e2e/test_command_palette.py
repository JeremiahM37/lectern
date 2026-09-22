"""Command search through the real app, plus actual tmux attachment."""
import json
from pathlib import Path
import subprocess
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_terminal_tabs import frame, ready


def search(page, text):
    page.get_by_role('button',name='Search sessions and actions',exact=True).click()
    page.get_by_role('combobox',name='Search sessions, tasks, and actions').fill(text)
    return page.get_by_role('dialog',name='Search Lectern')


def test_command_ranking_handles_multiple_terms_unicode_and_title_priority():
    script="""
import {rankCommands} from './src/shell/Palette.tsx';
const noop=()=>{};
const items=[{id:'a',title:'Open terminals',category:'Navigate',run:noop}, {id:'b',title:'Backend repair',category:'Sessions',detail:'Work/Client codex',run:noop}, {id:'c',title:'Work',category:'Tasks',run:noop}, {id:'d',title:'研究 API',category:'Sessions',run:noop}];
const assert=(ok:boolean)=>{if(!ok)throw Error('ranking mismatch')};
assert(rankCommands(items,'work')[0]?.id==='c');
assert(rankCommands(items,'client CODEX')[0]?.id==='b');
assert(rankCommands(items,'研究')[0]?.id==='d');
assert(rankCommands(items,'missing').length===0);
assert(rankCommands(items,'').length===4);
"""
    subprocess.run(['node','--import','tsx','--input-type=module','-e',script.replace('(ok:boolean)','(ok)')],cwd=Path('frontend'),check=True)


@pytest.mark.parametrize('width',[390,1440])
def test_command_search_attaches_and_keeps_terminal_alive(page,real_terminal,width):
    t=real_terminal;errors=[];page.on('pageerror',lambda error:errors.append(str(error)))
    page.set_viewport_size({'width':width,'height':900});page.goto(t['url'])
    dialog=search(page,'real terminal')
    expect(dialog.get_by_role('option')).to_have_count(1)
    if width==390:
        page.set_viewport_size({'width':390,'height':400})
        assert dialog.bounding_box()['y']+dialog.bounding_box()['height'] <= 400
        page.set_viewport_size({'width':390,'height':900})
    page.screenshot(path=f'/tmp/lectern-command-search-{width}.png')
    dialog.get_by_role('option').click();one=frame(page,t['id']);ready(one)
    one.locator('body').evaluate('()=>window.paletteTerminalIdentity="same-terminal"')
    search(page,'task board').get_by_role('option').click()
    expect(page.locator('.tab[data-tab="board"]')).to_have_class('tab on')
    search(page,'real terminal').get_by_role('option').click();ready(one)
    expect(page.locator('#terminal-workspace iframe')).to_have_count(1)
    assert one.locator('body').evaluate('()=>window.paletteTerminalIdentity')=='same-terminal'
    search(page,'notifications').get_by_role('option').click()
    expect(page.get_by_role('tab',name='Notifications',exact=True)).to_have_attribute('aria-selected','true')
    assert not errors


def test_command_search_keyboard_drafts_empty_results_and_refresh_error(page,real_terminal):
    t=real_terminal;page.goto(t['url'])
    page.keyboard.press('Control+k')
    box=page.get_by_role('combobox',name='Search sessions, tasks, and actions');expect(box).to_be_focused()
    box.fill('new');page.keyboard.press('ArrowDown');page.keyboard.press('Enter')
    expect(page.get_by_role('dialog',name='New task',exact=True)).to_be_visible()
    # Opening search over a form and escaping must preserve the form/draft.
    field=page.get_by_role('dialog',name='New task',exact=True).locator('input').first;field.fill('Keep this draft');field.focus()
    page.keyboard.press('Control+k');expect(box).to_be_focused()
    box.fill('nothing-matches-92836');expect(page.locator('#command-results').get_by_role('option')).to_have_count(0)
    page.keyboard.press('Enter');expect(page.get_by_role('dialog',name='Search Lectern')).to_be_visible()
    page.keyboard.press('Escape');expect(field).to_have_value('Keep this draft');expect(field).to_be_focused()
    expect(page.get_by_role('dialog',name='New task',exact=True)).to_be_visible()
    page.keyboard.press('Escape')
    # Exercise actual offline fetches, including when the service worker has
    # already taken control; page.route cannot reliably intercept that path.
    page.context.set_offline(True)
    dialog=search(page,'real terminal')
    expect(dialog.get_by_role('status')).to_contain_text('Could not refresh')
    expect(dialog.get_by_role('option')).to_have_count(1)
    page.keyboard.press('Tab');expect(dialog.get_by_role('button',name='Close search')).to_be_focused()
    page.keyboard.press('Tab');expect(box).to_be_focused()
    page.keyboard.press('Escape');expect(page.get_by_role('button',name='Search sessions and actions')).to_be_focused()


def test_command_search_touch_navigation_and_small_viewport(page,real_terminal):
    with page.context.browser.new_context(viewport={'width':390,'height':844},is_mobile=True,has_touch=True) as context:
        phone=context.new_page();phone.goto(real_terminal['url'])
        phone.get_by_role('button',name='Search sessions and actions').tap()
        dialog=phone.get_by_role('dialog',name='Search Lectern')
        dialog.get_by_role('combobox').fill('notifications')
        dialog.get_by_role('option').tap()
        expect(phone.get_by_role('tab',name='Notifications',exact=True)).to_have_attribute('aria-selected','true')
        phone.screenshot(path='/tmp/lectern-command-touch-navigation.png')
