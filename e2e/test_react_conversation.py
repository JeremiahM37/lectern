"""Conversation behavior against the actual React app and isolated Go backend."""
import re
import pytest
from playwright.sync_api import expect

def open_chat(page,server,title='Conversation regression'):
 project=page.request.get(server+'/api/projects').json()[0]['id']
 task=page.request.post(server+'/api/tasks',data={'project_id':project,'title':title,'prompt':'Read the context carefully'}).json()
 page.goto(server+"/#board")
 page.locator('.card',has_text=title).get_by_role('button',name=re.compile('Chat')).click()
 expect(page.locator('#conversation-log')).to_contain_text('Read the context carefully')
 return task

@pytest.mark.parametrize('width',[320,1440])
def test_draft_retry_identity_and_new_text_during_send(page,server,width):
 page.set_viewport_size({'width':width,'height':900})
 task=open_chat(page,server)
 box=page.locator('#conversation-input');box.fill('Keep this draft')
 page.locator('#conversation-close').click()
 page.locator('.card',has_text=task['title']).get_by_role('button',name=re.compile('Chat')).click()
 expect(box).to_have_value('Keep this draft')
 ids=[]
 def fail(route):
  if route.request.method=='POST':ids.append(route.request.post_data_json['request_id']);route.abort()
  else:route.continue_()
 path=f'**/api/tasks/{task["id"]}/messages'
 page.route(path,fail);page.locator('#conversation-send').click()
 expect(page.locator('#conversation-receipt')).to_contain_text('Your draft is kept')
 page.unroute(path,fail)
 held=[]
 def hold(route):
  if route.request.method=='POST':
   ids.append(route.request.post_data_json['request_id']);held.append((route,route.fetch()))
  else:route.continue_()
 page.route(path,hold);page.locator('#conversation-send').click()
 expect(page.locator('#conversation-receipt')).to_have_text('Sending…')
 box.fill('New text written during delivery')
 assert held and ids[0]==ids[1]
 held[0][0].fulfill(response=held[0][1]);page.unroute(path,hold)
 expect(box).to_have_value('New text written during delivery')
 expect(page.locator('#conversation-send')).to_be_enabled()
 assert page.locator('#conversation').evaluate('(e)=>e.scrollWidth<=innerWidth')
 page.locator('#conversation-close').click()


def test_attachment_only_upload_failure_paste_drop_and_send(page,server):
 task=open_chat(page,server,'Attachment regression')
 files=page.locator('#conversation-files');payload={'name':'notes.txt','mimeType':'text/plain','buffer':b'reference context'}
 endpoint=f'**/api/tasks/{task["id"]}/attachments'
 page.route(endpoint,lambda route:route.fulfill(status=502,content_type='application/json',body='{"detail":"disk full"}'))
 files.set_input_files(payload);expect(page.locator('#conversation-upload-status')).to_contain_text('disk full')
 page.unroute(endpoint);files.set_input_files(payload);expect(page.locator('#conversation-upload-status')).to_contain_text('Files ready')
 page.locator('#conversation-input').evaluate("""el=>{const data=new DataTransfer();data.items.add(new File(['image'],'pasted.png',{type:'image/png'}));el.dispatchEvent(new ClipboardEvent('paste',{clipboardData:data,bubbles:true,cancelable:true}));}""")
 expect(page.locator('#conversation-attachments')).to_contain_text('pasted.png');expect(page.locator('#conversation-upload-status')).to_contain_text('Files ready')
 page.locator('#conversation-compose').evaluate("""el=>{const data=new DataTransfer();data.items.add(new File(['sheet'],'dropped.csv',{type:'text/csv'}));el.dispatchEvent(new DragEvent('drop',{dataTransfer:data,bubbles:true,cancelable:true}));}""")
 expect(page.locator('#conversation-attachments')).to_contain_text('dropped.csv');expect(page.locator('#conversation-upload-status')).to_contain_text('Files ready')
 page.get_by_role('button',name='Remove pasted.png',exact=True).click()
 with page.expect_request(lambda request:request.url.endswith(f'/api/tasks/{task["id"]}/messages') and request.method=='POST') as sent:
  page.locator('#conversation-send').click()
 text=sent.value.post_data_json['text'];assert 'notes.txt' in text and 'dropped.csv' in text and 'pasted.png' not in text
 expect(page.locator('#conversation-attachments')).to_be_empty();expect(page.locator('#conversation-log')).to_contain_text('You · added to task')
 page.locator('#conversation-close').click()
