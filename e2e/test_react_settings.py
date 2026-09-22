from pathlib import Path
import subprocess,time,urllib.request
R=Path(__file__).parents[1]
def test_react_settings_fixture(browser):
 p=subprocess.Popen(['npm','exec','vite','--','--host','127.0.0.1','--port','4189'],cwd=R/'frontend',stdout=subprocess.DEVNULL,stderr=subprocess.STDOUT)
 try:
  url='http://127.0.0.1:4189/react/settings-harness.html'
  for _ in range(50):
   try:urllib.request.urlopen(url);break
   except Exception:time.sleep(.1)
  with browser.new_context() as context:
   x=context.new_page();x.goto(url);x.get_by_text('local',exact=True).wait_for();x.get_by_text('Probe').click();x.wait_for_function("calls.some(c=>String(c[0]).endsWith('/check'))")
   x.get_by_role('tab',name='Projects').click();x.locator('.pjrow').first.click();x.get_by_text('Save project').click();x.get_by_text('Skill A').wait_for();x.get_by_role('button',name='Attach',exact=True).click();x.wait_for_function("calls.some(c=>c[0]==='/projects/1/skills' && c[1]?.method==='POST')");x.wait_for_function("calls.some(c=>c[0]==='/projects/1')");x.locator('#sheet .x').click();x.get_by_placeholder('/home/you/projects').fill('/src');x.get_by_text('Scan').click();x.get_by_text('Found repo').wait_for();x.get_by_text('Import selected').click();x.wait_for_function("calls.some(c=>c[0]==='/projects/import')")
   x.get_by_role('tab',name='Notifications').click();x.get_by_text('Enable push on this device').click();x.get_by_text('Save sinks').click();x.get_by_text('Send test').click();x.wait_for_function("calls.some(c=>c[0]==='/settings/test-notification')")
   x.get_by_role('tab',name='Agents').click();x.locator('article').filter(has_text='runner-secret').get_by_text('Edit').click();x.get_by_label('Command',exact=True).fill('runner-v2');x.get_by_text('Save runner').click();x.wait_for_function("calls.some(c=>c[0]==='/agents' && (JSON.stringify(c[1])||'').includes('__lectern_retained'))");x.get_by_text('Add agent').click();x.get_by_label('Name').fill('custom');x.get_by_label('Command',exact=True).fill('runner');x.get_by_text('Save runner').click();x.wait_for_function("calls.some(c=>c[0]==='/agents' && c[1]?.method==='PUT')");x.get_by_text('Edit profile').click();x.get_by_label('Environment (JSON)').fill('{\"X\":\"1\"}');x.get_by_text('Save profile').click();x.wait_for_function("calls.some(c=>c[0]==='/launch-profiles/1' && c[1]?.method==='PUT')");x.get_by_text('Delete profile').click();x.wait_for_function("calls.some(c=>String(c[0]).startsWith('/launch-profiles/'))")
 finally:p.terminate();p.wait()
