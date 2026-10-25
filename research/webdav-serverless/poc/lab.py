#!/usr/bin/env python3
"""Loopback-only WebDAV redirect laboratory. No AWS access or production credentials."""
import argparse, base64, hashlib, hmac, json, os, re, secrets, threading, time
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
from pathlib import Path
from urllib.parse import urlsplit, parse_qs, quote
from xml.sax.saxutils import escape

SECRET = secrets.token_bytes(32)
AUTH = 'Basic ' + base64.b64encode(b'lab:lab-only').decode()
LOCK = threading.Lock()
ROOT = None
S3 = None
BUCKET = None

def event(**kw):
    # Deliberately never record URI queries, Location, Authorization values or bodies.
    with LOCK:
        with (ROOT / 'events.jsonl').open('a') as f:
            f.write(json.dumps(dict(t=time.time(), **kw)) + '\n')

def signature(method, name, expiry):
    return hmac.new(SECRET, f'{method}\n{name}\n{expiry}'.encode(), hashlib.sha256).hexdigest()

class Handler(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def log_message(self, *_): pass
    def handle_expect_100(self): return True  # Allow final response without sending 100.
    def reply(self, status, data=b'', headers=None):
        self.send_response(status)
        self.send_header('Content-Length', str(len(data)))
        self.send_header('Connection', 'close')
        self.send_header('Cache-Control', 'no-store')
        for k, v in (headers or {}).items(): self.send_header(k, v)
        self.end_headers()
        if self.command != 'HEAD': self.wfile.write(data)
        self.close_connection = True
    def run_request(self):
        parsed = urlsplit(self.path)
        m = re.fullmatch(r'/(r307|r308|buffer6|buffer10|sink)/zotero/([A-Za-z0-9_.-]*)', parsed.path)
        if not m: return self.reply(404)
        mode, name = m.groups()
        if name in ('.', '..'): return self.reply(400)
        length = int(self.headers.get('Content-Length', '0'))
        meta = dict(mode=mode, method=self.command, name=name, declared=length,
                    authorization_present=bool(self.headers.get('Authorization')),
                    content_type=self.headers.get('Content-Type'), expect=self.headers.get('Expect'),
                    transfer_encoding=self.headers.get('Transfer-Encoding'))
        if self.headers.get('Transfer-Encoding'): return self.reply(501)
        if mode == 'sink':
            q = parse_qs(parsed.query)
            try:
                expiry = int(q['expires'][0]); token = q['token'][0]
                valid = expiry >= time.time() and hmac.compare_digest(token, signature(self.command, name, expiry))
            except (KeyError, ValueError): valid = False
            if not valid: return self.reply(403)
            # S3 presigned requests must not carry WebDAV Basic auth.
            if self.headers.get('Authorization'):
                event(**meta, status=400, read=0); return self.reply(400)
        elif not hmac.compare_digest(self.headers.get('Authorization', ''), AUTH):
            event(**meta, status=401, read=0)
            return self.reply(401, headers={'WWW-Authenticate': 'Basic realm="Isolated lab"'})
        target = ROOT / 'objects' / name
        if mode != 'sink' and self.command in ('PUT', 'GET') and name.endswith('.zip'):
            read = 0
            if mode.startswith('buffer') and self.command == 'PUT':
                limit = int(mode[6:]) * 1024 * 1024
                if length > limit:
                    event(**meta, status=413, read=0); return self.reply(413)
                while read < length:
                    chunk = self.rfile.read(min(65536, length-read))
                    if not chunk: break
                    read += len(chunk)
            expiry = int(time.time()) + 120
            code = 308 if mode == 'r308' else 307
            url = f'http://localhost:{self.server.server_port}/sink/zotero/{quote(name)}?expires={expiry}&token={signature(self.command,name,expiry)}'
            if S3 is not None:
                operation = 'put_object' if self.command == 'PUT' else 'get_object'
                params = {'Bucket': BUCKET, 'Key': 'webdav-poc/zotero/' + name}
                if self.command == 'PUT': params['ContentType'] = self.headers.get('Content-Type', 'application/zip')
                url = S3.generate_presigned_url(operation, Params=params, ExpiresIn=120, HttpMethod=self.command)
            event(**meta, status=code, read=read)
            return self.reply(code, headers={'Location': url})
        if self.command == 'OPTIONS':
            return self.reply(200, headers={'DAV':'1', 'Allow':'OPTIONS, PROPFIND, MKCOL, HEAD, GET, PUT, DELETE'})
        if self.command == 'PROPFIND':
            self.rfile.read(length)
            names = [''] if not name else [name]
            if self.headers.get('Depth') == '1' and not name:
                names += [p.name for p in (ROOT/'objects').iterdir() if not p.name.endswith('.partial')]
            rows = ''.join('<D:response><D:href>' + escape(f'/{mode}/zotero/{n}') + '</D:href><D:propstat><D:prop><D:getcontentlength>0</D:getcontentlength><D:resourcetype>' + ('<D:collection/>' if not n else '') + '</D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>' for n in names)
            return self.reply(207, ('<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">'+rows+'</D:multistatus>').encode(), {'Content-Type':'application/xml'})
        if self.command == 'MKCOL': return self.reply(201)
        if self.command == 'PUT' and name:
            sha = hashlib.sha256(); read = 0
            temp = target.with_name(name + '.' + secrets.token_hex(5) + '.partial')
            with temp.open('wb') as f:
                while read < length:
                    chunk = self.rfile.read(min(65536, length-read))
                    if not chunk: break
                    f.write(chunk); sha.update(chunk); read += len(chunk)
            if read != length:
                temp.unlink(); event(**meta, status=400, read=read); return self.reply(400)
            os.replace(temp, target)
            event(**meta, status=200, read=read, sha256=sha.hexdigest())
            return self.reply(200)
        if self.command in ('GET','HEAD') and name:
            if not target.is_file(): return self.reply(404)
            data = target.read_bytes()
            event(**meta, status=200, read=0, sha256=hashlib.sha256(data).hexdigest())
            return self.reply(200, data, {'Content-Type':'application/octet-stream'})
        if self.command == 'DELETE' and name:
            if not target.is_file(): return self.reply(404)
            target.unlink(); return self.reply(204)
        return self.reply(405)
    do_GET = do_HEAD = do_PUT = do_DELETE = do_OPTIONS = do_PROPFIND = do_MKCOL = run_request

if __name__ == '__main__':
    p=argparse.ArgumentParser(); p.add_argument('--root',required=True); p.add_argument('--port',type=int,default=18765)
    p.add_argument('--s3-bucket', help='Opt-in only: existing disposable bucket ending in -webdav-poc')
    args=p.parse_args()
    if args.s3_bucket:
        if not args.s3_bucket.endswith('-webdav-poc'): p.error('Refusing non-lab bucket name')
        import boto3
        from botocore.config import Config
        BUCKET=args.s3_bucket
        S3=boto3.client('s3', config=Config(signature_version='s3v4', request_checksum_calculation='when_required', response_checksum_validation='when_required'))
    ROOT=Path(args.root).resolve(); (ROOT/'objects').mkdir(parents=True,exist_ok=True)
    server=ThreadingHTTPServer(('127.0.0.1',args.port),Handler)
    print('Loopback lab ready; synthetic credentials lab / lab-only', flush=True)
    server.serve_forever()
