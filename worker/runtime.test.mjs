import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {fileURLToPath} from 'node:url';
const {Miniflare} = await import(process.env.MINIFLARE_MODULE || 'miniflare');
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const signedURL='https://test-bucket.s3.us-east-2.amazonaws.com/zotero/ABCD1234.zip?X-Amz-Signature=synthetic';
for (const mode of ['stream','redirect']) {
  for (const size of [12,32]) {
    const bytes=new Uint8Array(size*1024*1024).fill(23);
    let metadataCalls=0, uploadCalls=0;
    const mf=new Miniflare({
      modules:true, scriptPath:fileURLToPath(new URL('./worker.mjs',import.meta.url)),
      compatibilityDate:'2026-07-01',
      bindings:{API_ORIGIN:'https://api.test',S3_HOST:'test-bucket.s3.us-east-2.amazonaws.com',WORKER_TOKEN:'x'.repeat(32),UPLOAD_MODE:mode},
      outboundService:async req => {
        if (req.url==='https://api.test/_worker/upload') {
          metadataCalls++;
          assert.equal(req.method,'POST');
          const text=await req.text();assert.ok(text.length<1024);
          const meta=JSON.parse(text);assert.equal(meta.contentLength,bytes.length);
          return Response.json({url:signedURL,headers:{'Content-Type':'application/zip','Content-Length':String(bytes.length)}});
        }
        assert.equal(req.url,signedURL);uploadCalls++;
        assert.equal(req.method,'PUT');
        assert.equal(req.headers.get('Authorization'),null);
        assert.equal(req.headers.get('X-WebDAV-Worker-Token'),null);
        assert.equal(req.headers.get('Cookie'),null);
        assert.equal(req.headers.get('Content-Type'),'application/zip');
        assert.equal(req.headers.get('Content-Length'),String(bytes.length));
        assert.equal(hash(new Uint8Array(await req.arrayBuffer())),hash(bytes));
        return new Response(null,{status:200});
      }
    });
    try {
      const response=await mf.dispatchFetch('https://worker.test/zotero/ABCD1234.zip',{
        method:'PUT',redirect:'manual',body:bytes,
        headers:{Authorization:'Basic synthetic','Content-Type':'application/zip','Content-Length':String(bytes.length),Cookie:'do-not-forward'}
      });
      assert.equal(response.status,mode==='stream'?200:307);
      assert.equal(metadataCalls,1);assert.equal(uploadCalls,mode==='stream'?1:0);
      console.log(`${mode} ${size} MiB passed (workerd local; AWS/S3 mocked)`);
    } finally {await mf.dispose();}
  }
}
