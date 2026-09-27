# Lectern cookie import (docs/browser.md). Runs on the browser's own machine:
# it reads cookies from a file or from a local Chrome profile there, decrypts
# them there, and hands them to the session's browser over its loopback
# DevTools port. Only counts are printed; no cookie leaves this machine.
#   cookies.py PORT WSPATH file PATH [DOMAINS]
#   cookies.py PORT WSPATH chrome PROFILE_DIR|auto [DOMAINS]
import base64, hashlib, json, os, shutil, socket, sqlite3, struct, subprocess, sys, tempfile

port, wspath, kind, source = int(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4]
domains = [d.strip().lstrip('.').lower() for d in (sys.argv[5] if len(sys.argv) > 5 else '').split(',') if d.strip()]
skipped = {}


def skip(reason):
    skipped[reason] = skipped.get(reason, 0) + 1


def wanted(domain):
    d = domain.lstrip('.').lower()
    return not domains or any(d == w or d.endswith('.' + w) for w in domains)


SAMESITE = {'strict': 'Strict', 'lax': 'Lax', 'none': 'None', 'no_restriction': 'None', 'unspecified': None}


def cookie(name, value, domain, path='/', expires=None, secure=False, http_only=False, same_site=None):
    c = {'name': name, 'value': value, 'domain': domain, 'path': path or '/', 'secure': bool(secure), 'httpOnly': bool(http_only)}
    if expires and expires > 0:
        c['expires'] = float(expires)
    if same_site:
        c['sameSite'] = same_site
    return c


def from_file(path):
    raw = open(path, 'rb').read().decode('utf-8', 'replace')
    out = []
    text = raw.strip()
    if text.startswith('[') or text.startswith('{'):
        data = json.loads(text)
        items = data.get('cookies', []) if isinstance(data, dict) else data
        for c in items:
            if not isinstance(c, dict) or 'name' not in c or 'value' not in c or not (c.get('domain') or c.get('host')):
                skip('not a cookie'); continue
            ss = c.get('sameSite') or c.get('same_site')
            ss = SAMESITE.get(str(ss).lower(), None) if ss is not None else None
            exp = c.get('expires', c.get('expirationDate'))
            out.append(cookie(c['name'], str(c['value']), c.get('domain') or c.get('host'), c.get('path', '/'),
                              exp if isinstance(exp, (int, float)) else None, c.get('secure'), c.get('httpOnly'), ss))
        return out
    for line in raw.splitlines():
        http_only = False
        if line.startswith('#HttpOnly_'):
            http_only, line = True, line[len('#HttpOnly_'):]
        if not line.strip() or line.startswith('#'):
            continue
        f = line.split('\t')
        if len(f) != 7:
            skip('malformed line'); continue
        domain, _, path, secure, expires, name, value = f
        out.append(cookie(name, value, domain, path, int(expires) if expires.isdigit() else None,
                          secure.upper() == 'TRUE', http_only))
    return out


def aes_cbc_decrypt(key, data):
    try:
        from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
        d = Cipher(algorithms.AES(key), modes.CBC(b' ' * 16)).decryptor()
        plain = d.update(data) + d.finalize()
    except ImportError:
        r = subprocess.run(['openssl', 'enc', '-d', '-aes-128-cbc', '-nopad', '-K', key.hex(), '-iv', (b' ' * 16).hex()],
                           input=data, capture_output=True)
        if r.returncode != 0:
            raise ValueError('openssl failed')
        plain = r.stdout
    pad = plain[-1] if plain else 0
    if not 1 <= pad <= 16:
        raise ValueError('bad padding')
    return plain[:-pad]


def keyring_password():
    for app in ('chrome', 'chromium', 'brave', 'microsoft-edge'):
        try:
            r = subprocess.run(['secret-tool', 'lookup', 'application', app], capture_output=True, timeout=5)
            if r.returncode == 0 and r.stdout.strip():
                return r.stdout.strip()
        except Exception:
            pass
    return None


def from_chrome(profile):
    if profile == 'auto':
        home = os.path.expanduser('~')
        for cand in ('.config/google-chrome/Default', '.config/chromium/Default', '.config/BraveSoftware/Brave-Browser/Default',
                     '.config/microsoft-edge/Default'):
            if os.path.isdir(os.path.join(home, cand)):
                profile = os.path.join(home, cand); break
        else:
            raise SystemExit(json.dumps({'error': 'no Chrome, Chromium, Brave or Edge profile found in ~/.config'}))
    db = None
    for cand in ('Network/Cookies', 'Cookies'):
        if os.path.isfile(os.path.join(profile, cand)):
            db = os.path.join(profile, cand); break
    if not db:
        raise SystemExit(json.dumps({'error': 'no Cookies database in ' + profile}))
    tmp = tempfile.mkdtemp(prefix='lectern-cookies-')
    try:
        copy = os.path.join(tmp, 'Cookies')
        shutil.copy2(db, copy)
        con = sqlite3.connect('file:' + copy + '?mode=ro', uri=True)
        try:
            version = int(con.execute("select value from meta where key='version'").fetchone()[0])
        except Exception:
            version = 0
        v10 = hashlib.pbkdf2_hmac('sha1', b'peanuts', b'saltysalt', 1, 16)
        pw = None
        v11 = None
        out = []
        for host, name, value, enc, path, expires, secure, http_only, samesite in con.execute(
                'select host_key,name,value,encrypted_value,path,expires_utc,is_secure,is_httponly,samesite from cookies'):
            if not wanted(host):
                continue
            if not value and enc:
                prefix = bytes(enc[:3])
                try:
                    if prefix == b'v10':
                        plain = aes_cbc_decrypt(v10, bytes(enc[3:]))
                    elif prefix == b'v11':
                        if v11 is None:
                            pw = keyring_password()
                            v11 = hashlib.pbkdf2_hmac('sha1', pw, b'saltysalt', 1, 16) if pw else b''
                        if not v11:
                            skip('encrypted with a desktop keyring this machine cannot open'); continue
                        plain = aes_cbc_decrypt(v11, bytes(enc[3:]))
                    else:
                        skip('unknown encryption'); continue
                    if version >= 24:
                        plain = plain[32:]
                    value = plain.decode('utf-8')
                except Exception:
                    skip('could not be decrypted'); continue
            exp = None
            if expires:
                exp = expires / 1e6 - 11644473600
            ss = {0: 'None', 1: 'Lax', 2: 'Strict'}.get(samesite)
            out.append(cookie(name, value, host, path, exp, secure, http_only, ss))
        con.close()
        return out
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


class WS:
    def __init__(self, port, path):
        self.s = socket.create_connection(('127.0.0.1', port), timeout=20)
        key = base64.b64encode(os.urandom(16)).decode()
        self.s.sendall(('GET %s HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n'
                        'Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n' % (path, port, key)).encode())
        head = b''
        while b'\r\n\r\n' not in head:
            chunk = self.s.recv(4096)
            if not chunk:
                raise SystemExit(json.dumps({'error': 'the browser closed the DevTools connection'}))
            head += chunk
        if b' 101 ' not in head.split(b'\r\n')[0]:
            raise SystemExit(json.dumps({'error': 'the browser refused the DevTools connection'}))
        self.buf = head.split(b'\r\n\r\n', 1)[1]
        self.next = 0

    def send(self, obj):
        data = json.dumps(obj).encode()
        mask = os.urandom(4)
        n = len(data)
        head = bytes([0x81])
        if n < 126:
            head += bytes([0x80 | n])
        elif n < 65536:
            head += bytes([0x80 | 126]) + struct.pack('>H', n)
        else:
            head += bytes([0x80 | 127]) + struct.pack('>Q', n)
        self.s.sendall(head + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(data)))

    def read(self, n):
        while len(self.buf) < n:
            chunk = self.s.recv(65536)
            if not chunk:
                raise SystemExit(json.dumps({'error': 'the browser closed the DevTools connection'}))
            self.buf += chunk
        out, self.buf = self.buf[:n], self.buf[n:]
        return out

    def recv(self):
        data = b''
        while True:
            b1, b2 = self.read(2)
            n = b2 & 0x7f
            if n == 126:
                n = struct.unpack('>H', self.read(2))[0]
            elif n == 127:
                n = struct.unpack('>Q', self.read(8))[0]
            data += self.read(n)
            if b1 & 0x80:
                return json.loads(data)

    def call(self, method, params):
        self.next += 1
        self.send({'id': self.next, 'method': method, 'params': params})
        while True:
            msg = self.recv()
            if msg.get('id') == self.next:
                if 'error' in msg:
                    raise SystemExit(json.dumps({'error': msg['error'].get('message', 'DevTools error')}))
                return msg.get('result')


try:
    cookies = from_file(source) if kind == 'file' else from_chrome(os.path.expanduser(source))
except SystemExit:
    raise
except Exception as e:
    raise SystemExit(json.dumps({'error': 'could not read the cookies: %s' % e}))
finally:
    if kind == 'file':
        try:
            os.remove(source)
        except OSError:
            pass
cookies = [c for c in cookies if wanted(c['domain'])]
ws = WS(port, wspath)
done = 0
for i in range(0, len(cookies), 200):
    batch = cookies[i:i + 200]
    try:
        ws.call('Storage.setCookies', {'cookies': batch})
        done += len(batch)
    except SystemExit:
        for c in batch:
            try:
                ws.call('Storage.setCookies', {'cookies': [c]})
                done += 1
            except SystemExit:
                skip('refused by the browser')
sites = sorted({c['domain'].lstrip('.') for c in cookies})
print(json.dumps({'imported': done, 'skipped': skipped, 'sites': len(sites)}))
