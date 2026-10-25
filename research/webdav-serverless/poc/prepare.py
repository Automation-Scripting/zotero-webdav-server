#!/usr/bin/env python3
import hashlib, json, os, pathlib, zipfile
root=pathlib.Path('/private/tmp/zotero-webdav-lab')
(root/'profile').mkdir(parents=True, exist_ok=True)
(root/'data').mkdir(exist_ok=True)
prefs={'extensions.zotero.dataDir':str(root/'data'),'extensions.zotero.useDataDir':True,
'extensions.zotero.sync.autoSync':False,'extensions.zotero.firstRun':False,
'app.update.enabled':False,'extensions.zotero.reportTranslationFailure':False}
(root/'profile'/'user.js').write_text(''.join(f'user_pref({json.dumps(k)}, {json.dumps(v)});\n' for k,v in prefs.items()))
manifest=[]
for size in [1, 7, 12, 32]:
 p=root/f'payload-{size}.zip'
 with zipfile.ZipFile(p,'w',compression=zipfile.ZIP_STORED) as z:
  with z.open('synthetic.bin','w') as f:
   for _ in range(size): f.write(os.urandom(1024*1024))
 manifest.append({'name':p.name,'bytes':p.stat().st_size,'sha256':hashlib.sha256(p.read_bytes()).hexdigest()})
(root/'manifest.json').write_text(json.dumps(manifest,indent=2))
print(root)
