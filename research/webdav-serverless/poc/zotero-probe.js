// Run in Tools > Developer > Run JavaScript, with "Run as async function" checked.
// Only the disposable profile created by prepare.py is accepted. No cloud account needed.
const lab = '/private/tmp/zotero-webdav-lab';
if (Zotero.DataDirectory.dir !== lab + '/data') throw new Error('Refusing non-lab data directory');
const auth = 'Basic ' + btoa('lab:lab-only');
const results = [];
for (const mode of ['r307', 'r308', 'buffer6', 'buffer10']) {
  for (const size of [1, 7, 12, 32]) {
    const name = `${mode}-${size}.zip`;
    const url = `http://127.0.0.1:18765/${mode}/zotero/${name}`;
    const body = await File.createFromFileName(`${lab}/payload-${size}.zip`);
    const row = { mode, size, bytes: body.size };
    try {
      const req = await Zotero.HTTP.request('PUT', url, {
        body, headers: {Authorization: auth, 'Content-Type': 'application/zip'},
        timeout: 0, errorDelayMax: 0
      });
      row.put = req.status;
      // Same download implementation as WebDAV.downloadFile in Zotero 10.0.6.
      const uri = Services.io.newURI(url).mutate().setUserPass('lab:lab-only').finalize();
      const downloaded = `${lab}/download-${name}`;
      const response = await Zotero.HTTP.download(uri, downloaded, {
        successCodes: [200,404], noCache: true, errorDelayMax: 0
      });
      row.get = response.status;
      row.downloadBytes = (await IOUtils.stat(downloaded)).size;
    } catch(e) {
      // Do not serialize error messages: they can contain signed URLs or credentials.
      row.errorType = e.name || 'Error'; row.status = e.status || 0;
    }
    results.push(row);
  }
}
await IOUtils.writeUTF8(lab + '/zotero-results.json', JSON.stringify({
  version: Zotero.version, dataDir: Zotero.DataDirectory.dir, results
}, null, 2));
return results;
