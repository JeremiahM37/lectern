"""Incremental target-local search of visible native conversation messages."""
import hashlib, json, os, secrets, sqlite3, time
from pathlib import Path
if 'native_record' not in globals():
    from native_records import native_record, native_metadata, LIMIT

LINE_LIMIT = 10 * 1024 * 1024

SCHEMA = '''
CREATE TABLE IF NOT EXISTS documents(
 id INTEGER PRIMARY KEY, path TEXT UNIQUE NOT NULL, cid TEXT NOT NULL, cwd TEXT NOT NULL,
 title TEXT NOT NULL, modified REAL NOT NULL, device INTEGER, inode INTEGER,
 observed INTEGER DEFAULT 0, stamp INTEGER DEFAULT 0, offset INTEGER DEFAULT 0,
 prefix_len INTEGER DEFAULT 0, prefix TEXT DEFAULT '', tail TEXT DEFAULT '',
 pending INTEGER DEFAULT 1, skipping INTEGER DEFAULT 0, oversized INTEGER DEFAULT 0, touched INTEGER DEFAULT 0);
CREATE TABLE IF NOT EXISTS failures(
 path TEXT PRIMARY KEY, device INTEGER, inode INTEGER, observed INTEGER, stamp INTEGER,
 changed INTEGER, reason TEXT NOT NULL, retry_after REAL, touched INTEGER);
CREATE TABLE IF NOT EXISTS messages(
 id INTEGER PRIMARY KEY, doc INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
 offset INTEGER NOT NULL, end_offset INTEGER NOT NULL, role TEXT NOT NULL, text TEXT NOT NULL, fingerprint TEXT NOT NULL, UNIQUE(doc,offset));
CREATE INDEX IF NOT EXISTS messages_doc ON messages(doc);
CREATE VIRTUAL TABLE IF NOT EXISTS message_search USING fts5(text,content=messages,content_rowid=id);
CREATE TRIGGER IF NOT EXISTS message_insert AFTER INSERT ON messages BEGIN
 INSERT INTO message_search(rowid,text) VALUES(new.id,new.text); END;
CREATE TRIGGER IF NOT EXISTS message_delete AFTER DELETE ON messages BEGIN
 INSERT INTO message_search(message_search,rowid,text) VALUES('delete',old.id,old.text); END;
'''

class NativeSearchIndex:
    def __init__(self, cache, home, agent):
        if agent not in ('claude','codex'): raise ValueError('Unsupported native agent')
        self.home=Path(home).expanduser().resolve();self.agent=agent
        # Keep indexes separate even when callers use a common private cache root.
        key=hashlib.sha256((agent+'\0'+str(self.home)).encode()).hexdigest()
        folder=Path(cache).expanduser()/'native-search-v2';folder.mkdir(mode=0o700,parents=True,exist_ok=True)
        os.chmod(folder,0o700)
        self.path=folder/(key+'.sqlite3')
        fd=os.open(self.path,os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600);os.fchmod(fd,0o600);os.close(fd)
        self.db=sqlite3.connect(self.path,timeout=3);self.db.row_factory=sqlite3.Row
        try:
            self.db.execute('PRAGMA foreign_keys=ON')
            if self.db.execute('PRAGMA user_version').fetchone()[0] not in (0,2):
                raise ValueError('Search cache uses an unsupported version')
            self.db.executescript(SCHEMA+'\nPRAGMA user_version=2;')
        except Exception:
            self.db.close();raise
    def close(self): self.db.close()
    def reset(self):
        # Derived data only: source conversations and other profile indexes stay intact.
        with self.db:
            self.db.execute('BEGIN IMMEDIATE')
            self.db.execute('DELETE FROM documents')
            self.db.execute('DELETE FROM failures')
    def files(self):
        base=self.home/('sessions' if self.agent=='codex' else 'projects')
        found={}
        for path in base.glob('**/*.jsonl' if self.agent=='codex' else '*/*.jsonl'):
            real=path.resolve()
            if base.resolve() not in real.parents or not real.is_file():continue
            found[str(real)]=real
            if len(found)>10000:raise ValueError('Native history exceeds the 10000-file discovery limit')
        return sorted(found.values(),key=lambda p:p.stat().st_mtime,reverse=True)
    @staticmethod
    def digest(file,start,length):
        file.seek(start);return hashlib.sha256(file.read(length)).hexdigest()
    def sync(self, byte_budget=16*1024*1024, seconds=2):
        deadline=time.monotonic()+seconds;used=0;issues=[];failed=set();files=self.files();names={str(p) for p in files}
        with self.db:
            self.db.execute('BEGIN IMMEDIATE')
            touched={r['path']:r['touched'] for r in self.db.execute('SELECT path,touched FROM documents')}
            touched.update({r['path']:r['touched'] for r in self.db.execute('SELECT path,touched FROM failures')})
            files.sort(key=lambda p:touched.get(str(p),0))
            for row in self.db.execute('SELECT path FROM failures').fetchall():
                if row['path'] not in names:self.db.execute('DELETE FROM failures WHERE path=?',(row['path'],))
            for row in self.db.execute('SELECT id,path FROM documents').fetchall():
                if row['path'] not in names:self.db.execute('DELETE FROM documents WHERE id=?',(row['id'],))
            for path in files:
                if used>=byte_budget or time.monotonic()>=deadline:break
                old=self.db.execute('SELECT * FROM documents WHERE path=?',(str(path),)).fetchone()
                st=None
                try:
                    st=path.stat()
                    failure=self.db.execute('SELECT * FROM failures WHERE path=?',(str(path),)).fetchone()
                    if failure and (failure['device'],failure['inode'],failure['observed'],failure['stamp'],failure['changed'])==(st.st_dev,st.st_ino,st.st_size,st.st_mtime_ns,st.st_ctime_ns) and failure['retry_after']>time.time():
                        failed.add(str(path));issues.append(failure['reason']);continue
                    self.db.execute('DELETE FROM failures WHERE path=?',(str(path),))
                    if old and not old['pending'] and st.st_size==old['observed'] and st.st_mtime_ns==old['stamp'] and st.st_ino==old['inode'] and st.st_dev==old['device']:continue
                    info=native_metadata(path,self.agent,max_bytes=LINE_LIMIT)
                    if info is None:
                        if old:self.db.execute('DELETE FROM documents WHERE id=?',(old['id'],))
                        self.failed(path,st,'A transcript has no valid native header')
                        failed.add(str(path));issues.append('A transcript has no valid native header');continue
                    with path.open('rb') as file:
                        reset=not old
                        if old:
                            reset=(st.st_ino!=old['inode'] or st.st_dev!=old['device'] or st.st_size<old['observed'] or info['id']!=old['cid'] or info['cwd']!=old['cwd'] or
                                (not old['pending'] and st.st_size==old['observed'] and st.st_mtime_ns!=old['stamp']) or
                                self.digest(file,0,old['prefix_len'])!=old['prefix'] or
                                self.digest(file,max(0,old['offset']-256),min(256,old['offset']))!=old['tail'])
                        if reset:
                            if old:self.db.execute('DELETE FROM documents WHERE id=?',(old['id'],))
                            cur=self.db.execute('INSERT INTO documents(path,cid,cwd,title,modified,device,inode) VALUES(?,?,?,?,?,?,?)',
                                (str(path),info['id'],info['cwd'],info['title'],st.st_mtime,st.st_dev,st.st_ino))
                            doc=cur.lastrowid;offset=0;skipping=0;oversized=0
                        else:doc=old['id'];offset=old['offset'];skipping=old['skipping'];oversized=old['oversized']
                        file.seek(offset);pending=0
                        while offset<st.st_size:
                            if used>=byte_budget or time.monotonic()>=deadline:pending=1;break
                            line=file.readline(min(LINE_LIMIT+1,st.st_size-offset));used+=len(line)
                            if not line:break
                            if skipping:
                                offset+=len(line);skipping=int(not line.endswith(b'\n'));continue
                            if len(line)>LINE_LIMIT:
                                oversized+=1;offset+=len(line);skipping=int(not line.endswith(b'\n'));continue
                            if not line.endswith(b'\n'):break # retry this partial entry after append
                            position=offset;offset+=len(line)
                            try:row=json.loads(line)
                            except (ValueError,UnicodeError):continue
                            if not isinstance(row,dict):continue
                            message=native_record(row,self.agent,limit=None)
                            if message:self.db.execute('INSERT OR IGNORE INTO messages(doc,offset,end_offset,role,text,fingerprint) VALUES(?,?,?,?,?,?)',(doc,position,offset,message['role'],message['text'],message_fingerprint(message)))
                        prefix_len=min(offset,4096)
                        self.db.execute('UPDATE documents SET title=?,modified=?,observed=?,stamp=?,offset=?,prefix_len=?,prefix=?,tail=?,pending=?,skipping=?,oversized=?,touched=? WHERE id=?',
                            (info['title'],st.st_mtime,st.st_size,st.st_mtime_ns,offset,prefix_len,self.digest(file,0,prefix_len),self.digest(file,max(0,offset-256),min(256,offset)),pending,skipping,oversized,time.time_ns(),doc))
                except (OSError,ValueError,TypeError) as error:
                    # Include any replacement row allocated during this attempt.
                    self.db.execute('DELETE FROM documents WHERE path=?',(str(path),))
                    reason=type(error).__name__+' reading a transcript'
                    if st:self.failed(path,st,reason)
                    failed.add(str(path));issues.append(reason)
        rows={r['path']:r for r in self.db.execute('SELECT path,pending,observed,stamp FROM documents')}
        pending=0
        for path in files:
            if str(path) in failed:continue
            try:
                st=path.stat();row=rows.get(str(path))
                if row is None or row['pending'] or row['observed']!=st.st_size or row['stamp']!=st.st_mtime_ns:pending+=1
            except OSError:pending+=1
        return dict(complete=pending==0, pending_files=pending, scanned_bytes=used, documents=len(rows),
            oversized_entries=self.db.execute('SELECT COALESCE(sum(oversized),0) FROM documents').fetchone()[0],issues=sorted(set(issues)))
    def failed(self,path,st,reason):
        self.db.execute('INSERT OR REPLACE INTO failures VALUES(?,?,?,?,?,?,?,?,?)',
            (str(path),st.st_dev,st.st_ino,st.st_size,st.st_mtime_ns,st.st_ctime_ns,reason,time.time()+30,time.time_ns()))
    def search(self, query, limit=50):
        terms=query.strip().split()
        if not terms or len(query)>500:raise ValueError('Enter between 1 and 500 characters')
        match=' AND '.join('"'+term.replace('"','""')+'"' for term in terms)
        limit=min(max(int(limit),1),100)
        marker='lec-'+secrets.token_hex(12)
        opening,closing='['+marker+']','[/'+marker+']'
        rows=self.db.execute("""WITH hits AS (
            SELECT min(m.id) AS id FROM message_search
            JOIN messages m ON m.id=message_search.rowid
            WHERE message_search MATCH ? GROUP BY m.doc)
            SELECT d.id AS document,d.cid,d.cwd,d.title,d.modified,m.role,m.offset,m.end_offset,m.fingerprint,
            snippet(message_search,0,?,?, ' … ',32) AS snippet
            FROM message_search JOIN messages m ON m.id=message_search.rowid
            JOIN documents d ON d.id=m.doc JOIN hits ON hits.id=m.id
            WHERE message_search MATCH ? ORDER BY d.modified DESC LIMIT ?""",(match,opening,closing,match,limit+1)).fetchall()
        matches=[]
        for row in rows[:limit]:
            item=dict(row);item['snippet']=marked_excerpt(item['snippet'],opening,closing,1200)[0];matches.append(item)
        return dict(matches=matches,more=len(rows)>limit)


def message_fingerprint(message):
    return hashlib.sha256((message['role']+'\0'+message['text']).encode()).hexdigest()


def marked_excerpt(text, opening, closing, limit):
    position=text.find(opening)
    clean=text.replace(opening,'').replace(closing,'')
    if len(clean)<=limit:return clean,False
    start=max(0,(position if position>=0 else 0)-min(200,limit//4))
    return ('… ' if start else '')+clean[start:start+limit]+(' …' if start+limit<len(clean) else ''),True


def main():
    import argparse
    parser=argparse.ArgumentParser(description='Index and search visible native conversation text on this target')
    parser.add_argument('agent',choices=('claude','codex'))
    parser.add_argument('query')
    parser.add_argument('--reset',action='store_true',help='Rebuild this derived index; source histories are unchanged')
    args=parser.parse_args()
    home=os.path.expanduser(os.environ.get('CODEX_HOME','~/.codex') if args.agent=='codex' else os.environ.get('CLAUDE_CONFIG_DIR','~/.claude'))
    cache=os.environ.get('LECTERN_NATIVE_SEARCH_CACHE') or str(Path(os.environ.get('XDG_CACHE_HOME',str(Path.home()/'.cache')))/'lectern')
    index=None
    try:
        if not args.query.strip() or len(args.query)>500:raise ValueError('Enter between 1 and 500 characters')
        index=NativeSearchIndex(cache,home,args.agent)
        if args.reset:index.reset()
        progress=index.sync()
        print(json.dumps(dict(agent=args.agent,profile_key=index.path.stem,progress=progress,**index.search(args.query)),ensure_ascii=False))
    except (OSError,ValueError,sqlite3.Error) as error:
        print(json.dumps(dict(error=str(error))));return 1
    finally:
        if index:index.close()
    return 0

if __name__=='__main__' and not globals().get('NATIVE_SEARCH_LIBRARY'):
    raise SystemExit(main())
