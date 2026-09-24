from pathlib import Path
import subprocess,time,urllib.request
from session_sheet import open_advanced
ROOT=Path(__file__).parents[1]
def test_react_sessions_fixture(browser):
 p=subprocess.Popen(['npm','exec','vite','--','--host','127.0.0.1','--port','4188'],cwd=ROOT/'frontend',stdout=subprocess.DEVNULL,stderr=subprocess.STDOUT)
 try:
  for _ in range(50):
   try:urllib.request.urlopen('http://127.0.0.1:4188/react/sessions-harness.html');break
   except Exception:time.sleep(.1)
  with browser.new_context() as context:
   page=context.new_page();page.goto('http://127.0.0.1:4188/react/sessions-harness.html');page.get_by_text('Main work').wait_for()
   page.get_by_text('⌨ Attach').click();page.wait_for_function("calls.some(x=>x[0]==='terminal')")
   page.get_by_text('Chat').click();page.get_by_text('Live terminal text').wait_for();page.locator('#conversation-input').fill('continue');page.get_by_text('Send',exact=True).click();page.wait_for_function("calls.some(x=>String(x[0]).endsWith('/send'))");page.get_by_role('button',name='Close conversation').click()
   page.locator('.scard .action-menu>summary').click();page.get_by_text('⇥ Handoff').click();page.locator('#ho-mode').select_option('note');page.locator('#ho-go').click();page.wait_for_function("calls.some(x=>String(x[0]).endsWith('/handoff'))")
   page.locator('#sess-discover').click();page.get_by_text('outside').wait_for();page.get_by_role('button',name='Adopt').click();page.wait_for_function("calls.some(x=>x[0]==='/sessions/adopt')")
   page.get_by_text('+ New session').click();open_advanced(page);page.get_by_label('Name').fill('Fresh');page.get_by_text('▶ Start session').click();page.wait_for_function("calls.filter(x=>x[0]==='/sessions').length>0")
   page.locator('.scard .action-menu>summary').click();page.get_by_role('button',name='Saved conversations',exact=True).click();page.locator('.nh-select').select_option('abc');page.get_by_text('Saved answer').wait_for();page.get_by_role('button',name='Close',exact=True).click()
   page.locator('#sess-saved-search').click();page.get_by_placeholder('Find something discussed…').fill('needle');page.get_by_text('Search',exact=True).click();page.locator('.ns-result').click();page.get_by_text('Search answer').wait_for()
 finally:p.terminate();p.wait()
