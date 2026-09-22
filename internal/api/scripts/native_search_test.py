import json,os,sqlite3,subprocess,sys,tempfile,time,unittest,uuid
from unittest.mock import patch
import native_search as search_module
from contextlib import closing
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from native_search import NativeSearchIndex
from native_search_read import read_match

class SearchTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name);self.home=self.root/'profile';self.cache=self.root/'cache';self.cwd=str(self.root/'workspace')
        self.index=NativeSearchIndex(self.cache,self.home,'codex');self.addCleanup(self.index.close)
    def write(self,texts,cid=None):
        cid=cid or str(uuid.uuid4());path=self.home/'sessions'/(cid+'.jsonl');path.parent.mkdir(parents=True,exist_ok=True)
        rows=[{'type':'session_meta','payload':{'id':cid,'cwd':self.cwd,'source':'cli'}}]
        rows.extend(self.message(t) for t in texts)
        path.write_text(''.join(json.dumps(r)+'\n' for r in rows));return path,cid
    @staticmethod
    def message(text,channel='final',role='assistant'):
        return {'type':'response_item','payload':{'type':'message','role':role,'channel':channel,'content':[{'type':'output_text','text':text}]}}
    def matches(self,query):return self.index.search(query)['matches']
    def test_old_text_beyond_reader_window_and_long_message(self):
        path,cid=self.write(['rare opening discussion','x'*70000+' long-message-needle']+['ordinary filler '*200]*900)
        state=self.index.sync(seconds=10)
        self.assertTrue(state['complete']);self.assertGreater(path.stat().st_size,2*1024*1024)
        self.assertEqual(self.matches('rare opening')[0]['cid'],cid)
        hit=self.matches('long-message-needle')[0]
        self.assertEqual(hit['cid'],cid);self.assertIn('long-message-needle',hit['snippet']);self.assertLess(len(hit['snippet']),1210)
        self.assertGreater(self.matches('rare')[0]['end_offset'],self.matches('rare')[0]['offset'])
    def read_hit(self,hit,query):
        return read_match(self.index,hit['document'],hit['cid'],hit['cwd'],hit['offset'],hit['fingerprint'],query)
    def test_read_exact_old_match_with_bounded_context(self):
        path,cid=self.write(['before '+str(i) for i in range(8)]+['exact needle']+['after '+str(i) for i in range(8)]+['filler '*1000]*400)
        self.index.sync(seconds=10)
        result=self.read_hit(self.matches('exact needle')[0],'exact needle')
        self.assertEqual(result['conversation']['id'],cid)
        self.assertEqual(len(result['messages']),11)
        self.assertEqual([m['text'] for m in result['messages'] if m['matched']],['exact needle'])
        self.assertEqual(result['context_before_count'],5);self.assertEqual(result['context_after_count'],5)
        self.assertFalse(self.index.db.in_transaction)
        self.assertEqual(self.index.sync()['scanned_bytes'],0)
    def test_read_rejects_stale_message_and_releases_transaction(self):
        path,cid=self.write(['exact needle']);self.index.sync();hit=self.matches('needle')[0]
        path.write_text(path.read_text().replace('exact needle','other needle'))
        with self.assertRaisesRegex(ValueError,'Matched message changed'):self.read_hit(hit,'needle')
        self.assertFalse(self.index.db.in_transaction)
    def test_read_rejects_replaced_file_and_foreign_symlink(self):
        path,cid=self.write(['exact needle']);self.index.sync();hit=self.matches('needle')[0]
        moved=path.with_suffix('.old');path.rename(moved);path.write_bytes(moved.read_bytes())
        with self.assertRaisesRegex(ValueError,'file changed'):self.read_hit(hit,'needle')
        path.unlink();outside=self.root/'outside.jsonl';outside.write_bytes(moved.read_bytes());path.symlink_to(outside)
        with self.assertRaisesRegex(ValueError,'outside this native profile'):self.read_hit(hit,'needle')
    def test_read_long_message_centers_on_match(self):
        self.write(['x'*90000+' exactneedle '+'y'*90000]);self.index.sync(seconds=10)
        result=self.read_hit(self.matches('exactneedle')[0],'exactneedle')
        message=result['messages'][0]
        self.assertTrue(message['truncated']);self.assertTrue(message['matched'])
        self.assertIn('exactneedle',message['text']);self.assertLess(len(message['text']),64010)
    def test_read_skips_changed_neighbor_and_private_content(self):
        path,cid=self.write(['before words','exact needle','after words'])
        with path.open('a') as f:f.write(json.dumps(self.message('PRIVATE_SENTINEL',channel='analysis'))+'\n')
        self.index.sync();hit=self.matches('needle')[0]
        path.write_text(path.read_text().replace('before words','edited words'))
        result=self.read_hit(hit,'needle')
        self.assertEqual(result['changed_neighbors'],1)
        self.assertEqual([m['text'] for m in result['messages']],['exact needle','after words'])
    def test_read_pages_cover_history_and_revalidate_original_match(self):
        path,cid=self.write(['message '+str(i)+(' needle' if i==20 else '') for i in range(50)])
        self.index.sync();hit=self.matches('needle')[0]
        def page(mode='match',anchor=None):
            return read_match(self.index,hit['document'],cid,hit['cwd'],hit['offset'],hit['fingerprint'],'needle',mode,anchor)
        current=page();self.assertEqual(len(current['messages']),11)
        seen=[m['text'] for m in current['messages']]
        earlier=current
        while earlier['before'] is not None:
            earlier=page('before',earlier['before']);seen=[m['text'] for m in earlier['messages']]+seen
        later=current
        while later['after'] is not None:
            later=page('after',later['after']);seen.extend(m['text'] for m in later['messages'])
        self.assertEqual(seen,['message '+str(i)+(' needle' if i==20 else '') for i in range(50)])
        latest=page('latest');self.assertEqual(latest['messages'][-1]['text'],'message 49');self.assertIsNone(latest['after'])
        with self.assertRaisesRegex(ValueError,'boundary'):page('before',1)
        path.write_text(path.read_text().replace('20 needle','20 edited'))
        with self.assertRaisesRegex(ValueError,'Matched message changed'):page('latest')
    def test_latest_reports_unindexed_append(self):
        path,cid=self.write(['needle']);self.index.sync();hit=self.matches('needle')[0]
        with path.open('a') as f:f.write(json.dumps(self.message('new text'))+'\n')
        result=read_match(self.index,hit['document'],cid,hit['cwd'],hit['offset'],hit['fingerprint'],'needle','latest')
        self.assertFalse(result['index_complete']);self.assertEqual(len(result['messages']),1)
    def test_budgeted_resume_append_partial_and_no_duplicates(self):
        path,cid=self.write(['first needle','second needle'])
        state=self.index.sync(byte_budget=1,seconds=10);self.assertFalse(state['complete'])
        for _ in range(10):
            if self.index.sync(byte_budget=200,seconds=10)['complete']:break
        self.assertEqual(len(self.matches('needle')),1)
        count=self.index.db.execute('select count(*) from messages').fetchone()[0]
        partial=json.dumps(self.message('appended zebra'))
        with path.open('a') as f:f.write(partial[:-3])
        self.assertTrue(self.index.sync()['complete']);self.assertEqual(self.matches('zebra'),[])
        with path.open('a') as f:f.write(partial[-3:]+'\n')
        self.index.sync();self.assertEqual(self.matches('zebra')[0]['cid'],cid)
        self.assertEqual(self.index.db.execute('select count(*) from messages').fetchone()[0],count+1)
        self.assertEqual(self.index.sync()['scanned_bytes'],0)
    def test_budget_progress_is_fair_between_conversations(self):
        first,cid1=self.write(['one']*100)
        second,cid2=self.write(['two']*100)
        self.index.sync(byte_budget=1,seconds=10)
        self.index.sync(byte_budget=1,seconds=10)
        self.assertEqual(self.index.db.execute('select count(*) from documents').fetchone()[0],2)
    def test_private_channels_are_never_indexed(self):
        path,cid=self.write(['public visible résumé'])
        with path.open('a') as f:
            for channel in ('analysis','justify','confidence','summary','unknown-private'):f.write(json.dumps(self.message('PRIVATE_SENTINEL',channel=channel))+'\n')
            f.write(json.dumps(self.message('SYSTEM_SENTINEL',role='system'))+'\n')
        self.index.sync();self.assertEqual(self.matches('PRIVATE_SENTINEL'),[]);self.assertEqual(self.matches('SYSTEM_SENTINEL'),[])
        self.assertEqual(self.matches('résumé')[0]['cid'],cid)
        self.assertNotIn('PRIVATE_SENTINEL',str(self.index.db.execute('select text from messages').fetchall()))
    def test_rewrite_delete_and_replacement_invalidate_results(self):
        path,cid=self.write(['original wording']);self.index.sync()
        self.write(['replaced wording'],cid);self.index.sync()
        self.assertEqual(self.matches('original'),[]);self.assertEqual(len(self.matches('replaced')),1)
        path.unlink();self.index.sync();self.assertEqual(self.matches('replaced'),[])
    def test_fork_header_and_profile_boundaries(self):
        path,cid=self.write(['fork context'])
        rows=path.read_text().splitlines();rows.insert(1,json.dumps({'type':'session_meta','payload':{'id':str(uuid.uuid4()),'cwd':'/parent'}}));path.write_text('\n'.join(rows)+'\n')
        outside=self.root/'foreign.jsonl';outside.write_text(path.read_text().replace('fork context','foreign secret'))
        (path.parent/'linked.jsonl').symlink_to(outside)
        self.index.sync();self.assertEqual(self.matches('fork')[0]['cid'],cid);self.assertEqual(self.matches('foreign'),[])
        other=NativeSearchIndex(self.cache,self.root/'other-profile','codex');self.addCleanup(other.close)
        other.sync();self.assertEqual(other.search('fork')['matches'],[])
    def test_permissions_query_syntax_and_limit(self):
        self.write(['literal OR phrase']);self.write(['literal second conversation']);self.index.sync()
        self.assertEqual(self.index.path.stat().st_mode&0o777,0o600)
        self.assertEqual(self.index.path.parent.stat().st_mode&0o777,0o700)
        result=self.index.search('literal',1);self.assertEqual(len(result['matches']),1);self.assertTrue(result['more'])
        self.assertEqual(self.matches('missing OR literal'),[])
        self.matches('"literal"');self.matches('literal*')
    def test_claude_skips_thinking_and_sidechains(self):
        index=NativeSearchIndex(self.cache,self.home,'claude');self.addCleanup(index.close)
        cid=str(uuid.uuid4());folder=self.home/'projects'/'workspace';folder.mkdir(parents=True)
        base=dict(type='assistant',sessionId=cid,cwd=self.cwd)
        rows=[{**base,'message':{'role':'assistant','content':[{'type':'thinking','thinking':'THINKING_SENTINEL'},{'type':'text','text':'Visible Claude discussion'}]}},
              {**base,'isSidechain':True,'message':{'role':'assistant','content':'SIDECHAIN_SENTINEL'}}]
        (folder/(cid+'.jsonl')).write_text(''.join(json.dumps(r)+'\n' for r in rows))
        index.sync();self.assertEqual(index.search('Visible')['matches'][0]['cid'],cid)
        self.assertEqual(index.search('THINKING_SENTINEL')['matches'],[]);self.assertEqual(index.search('SIDECHAIN_SENTINEL')['matches'],[])
    def test_malformed_text_block_does_not_poison_visible_history(self):
        path,cid=self.write(['visible before'])
        row=self.message('visible after')
        row['payload']['content'].insert(0,{'type':'output_text','text':{'invalid':'not a string'}})
        with path.open('a') as f:f.write(json.dumps(row)+'\n')
        self.index.sync();self.assertEqual(self.matches('visible after')[0]['cid'],cid)
    def test_large_image_caption_is_searchable_without_indexing_image_data(self):
        index=NativeSearchIndex(self.cache,self.home,'claude');self.addCleanup(index.close)
        cid=str(uuid.uuid4());folder=self.home/'projects'/'large-image';folder.mkdir(parents=True)
        row={'type':'user','sessionId':cid,'cwd':self.cwd,'message':{'role':'user','content':[{'type':'image','source':{'data':'x'*(3*1024*1024)}},{'type':'text','text':'Large image caption needle'}]}}
        (folder/(cid+'.jsonl')).write_text(json.dumps(row)+'\n')
        index.sync(seconds=10);self.assertEqual(index.search('caption needle')['matches'][0]['cid'],cid)
        self.assertLess(index.path.stat().st_size,100000)
        hit=index.search('caption needle')['matches'][0]
        result=read_match(index,hit['document'],cid,hit['cwd'],hit['offset'],hit['fingerprint'],'caption needle')
        self.assertIn('Large image caption needle',result['messages'][0]['text'])
        self.assertLess(len(result['messages'][0]['text']),100)
    def test_oversized_entry_does_not_hide_following_messages(self):
        path,cid=self.write(['x'*(11*1024*1024),'following sentinel'])
        for _ in range(10):
            state=self.index.sync(byte_budget=1024,seconds=10)
            if state['complete']:break
        self.assertTrue(state['complete']);self.assertEqual(state['oversized_entries'],1)
        self.assertEqual(self.matches('following sentinel')[0]['cid'],cid)
    def test_invalid_header_finishes_with_an_issue(self):
        path,cid=self.write(['visible']);path.write_text('{}\n')
        state=self.index.sync();self.assertTrue(state['complete']);self.assertTrue(state['issues'])
    def test_concurrent_incremental_writers_do_not_duplicate_messages(self):
        self.write(['concurrent needle']*100)
        def worker():
            index=NativeSearchIndex(self.cache,self.home,'codex')
            try:
                for _ in range(200):
                    if index.sync(byte_budget=500,seconds=1)['complete']:break
            finally:index.close()
        with ThreadPoolExecutor(max_workers=3) as pool:list(pool.map(lambda _:worker(),range(3)))
        self.assertEqual(self.index.db.execute('select count(*) from messages').fetchone()[0],100)
        self.assertEqual(len(self.matches('concurrent')),1)
    def test_explicit_rebuild_discards_old_index_without_changing_sources(self):
        path,cid=self.write(['original wording']);self.index.sync()
        original=path.read_bytes();self.index.reset()
        self.assertEqual(path.read_bytes(),original);self.assertEqual(self.matches('original'),[])
        self.index.sync();self.assertEqual(self.matches('original')[0]['cid'],cid)
    def test_json_worker_uses_selected_profile_and_cache(self):
        path,cid=self.write(['worker phrase'])
        script=Path(__file__).with_name('native_search.py')
        env={**os.environ,'CODEX_HOME':str(self.home),'LECTERN_NATIVE_SEARCH_CACHE':str(self.cache)}
        reply=json.loads(subprocess.check_output([sys.executable,str(script),'codex','worker phrase'],env=env))
        self.assertTrue(reply['progress']['complete']);self.assertEqual(reply['matches'][0]['cid'],cid)
        reply=json.loads(subprocess.check_output([sys.executable,str(script),'--reset','codex','--','worker phrase'],env=env))
        self.assertEqual(reply['matches'][0]['cid'],cid)
        result=subprocess.run([sys.executable,str(script),'codex',' '],env=env,capture_output=True,text=True)
        self.assertEqual(result.returncode,1);self.assertIn('error',json.loads(result.stdout))
    def test_embedded_read_worker_and_profile_change(self):
        path,cid=self.write(['worker exact phrase']);self.index.sync();hit=self.matches('exact')[0]
        folder=Path(__file__).parent
        script='NATIVE_SEARCH_LIBRARY=True\n'+'\n'.join((folder/name).read_text() for name in ('native_records.py','native_search.py','native_search_read.py'))
        env={**os.environ,'CODEX_HOME':str(self.home),'LECTERN_NATIVE_SEARCH_CACHE':str(self.cache)}
        args=[sys.executable,'-c',script,'codex',str(hit['document']),cid,hit['cwd'],str(hit['offset']),hit['fingerprint'],self.index.path.stem,'exact']
        reply=json.loads(subprocess.check_output(args,env=env,cwd=self.root))
        self.assertEqual(reply['messages'][0]['text'],'worker exact phrase')
        env['CODEX_HOME']=str(self.root/'another-profile')
        result=subprocess.run(args,env=env,cwd=self.root,capture_output=True,text=True)
        self.assertEqual(result.returncode,1);self.assertIn('profile changed',json.loads(result.stdout)['error'])
    def test_slow_bad_header_cannot_starve_other_conversations(self):
        good,cid=self.write(['available discussion'])
        bad,_=self.write(['invalid']);bad.write_text('{}\n');os.utime(bad,(time.time()+60,)*2)
        original=search_module.native_metadata
        def metadata(path,*args,**kwargs):
            if path==bad:time.sleep(.02)
            return original(path,*args,**kwargs)
        with patch.object(search_module,'native_metadata',side_effect=metadata):
            self.assertFalse(self.index.sync(seconds=.01)['complete'])
            state=self.index.sync(seconds=.01)
            self.assertTrue(state['complete']);self.assertTrue(state['issues'])
        self.assertEqual(self.matches('available')[0]['cid'],cid)
        self.write(['recovered header'],bad.stem);self.index.sync()
        self.assertEqual(len(self.matches('recovered')),1)
    def test_failed_initial_scan_removes_partial_cached_matches(self):
        path,cid=self.write(['first message','second message'])
        original=search_module.native_record;calls=0
        def record(*args,**kwargs):
            nonlocal calls
            calls+=1
            if calls==3:raise TypeError('injected decoder failure')
            return original(*args,**kwargs)
        with patch.object(search_module,'native_record',side_effect=record):
            state=self.index.sync();self.assertTrue(state['issues'])
        self.assertEqual(self.matches('first'),[])
        self.index.reset();self.index.sync();self.assertEqual(self.matches('first')[0]['cid'],cid)
    def test_future_cache_version_is_preserved(self):
        other=NativeSearchIndex(self.cache,self.root/'future','codex');path=other.path
        other.db.execute('PRAGMA user_version=99');other.close()
        with self.assertRaisesRegex(ValueError,'unsupported version'):NativeSearchIndex(self.cache,self.root/'future','codex')
        with closing(sqlite3.connect(path)) as db:self.assertEqual(db.execute('PRAGMA user_version').fetchone()[0],99)

if __name__=='__main__':unittest.main()
