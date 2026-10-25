#!/usr/bin/env python3
"""Tests the local lab only, never claims to emulate Zotero/Gecko or AWS."""
import base64, hashlib, http.client, json, pathlib, socket, subprocess
root=pathlib.Path('/private/tmp/zotero-webdav-lab')
auth='Basic '+base64.b64encode(b'lab:lab-only').decode()
results=[]
# A declared 32 MiB PUT, zero body sent: does the local server respond immediately?
for mode in ['r307','r308','buffer6','buffer10']:
 with socket.create_connection(('127.0.0.1',18765),timeout=3) as s:
  s.sendall(f'PUT /{mode}/zotero/headers-only.zip HTTP/1.1\r\nHost: 127.0.0.1\r\nAuthorization: {auth}\r\nContent-Length: 33554432\r\nExpect: 100-continue\r\n\r\n'.encode())
  status=int(s.recv(4096).split(b' ')[1]); expected=307 if mode=='r307' else 308 if mode=='r308' else 413
  assert status==expected,(mode,status)
  results.append(dict(test='headers_only_zero_body',mode=mode,status=status))
for mode in ['r307','r308','buffer6','buffer10']:
 for size in [1,7,12,32]:
  name=f'curl-{mode}-{size}.zip'; src=root/f'payload-{size}.zip'
  # Never use -v or --location-trusted: both can expose credentials/signed URL.
  r=subprocess.run(['curl','--silent','--location','--max-time','30','--user','lab:lab-only',
    '--header','Content-Type: application/zip','--upload-file',str(src),
    '--output','/dev/null','--write-out','%{http_code}',
    f'http://127.0.0.1:18765/{mode}/zotero/{name}'],capture_output=True)
  status=int(r.stdout or 0)
  expected=413 if mode.startswith('buffer') and size>int(mode[6:]) else 200
  assert status==expected,(mode,size,status,r.returncode)
  if status==200:
   assert hashlib.sha256(src.read_bytes()).digest()==hashlib.sha256((root/'objects'/name).read_bytes()).digest()
  results.append(dict(test='curl_put',mode=mode,size=size,status=status))
(root/'smoke-results.json').write_text(json.dumps(results,indent=2))
print(f'{len(results)} cases passed; curl is NOT Zotero, buffer modes are NOT AWS')
