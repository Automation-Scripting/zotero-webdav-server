import test from 'node:test';
import assert from 'node:assert/strict';
import worker, {handle} from './worker.mjs';

const env = {API_ORIGIN:'https://api.example.test', S3_HOST:'bucket.s3.us-east-2.amazonaws.com', WORKER_TOKEN:'a'.repeat(32), UPLOAD_MODE:'redirect'};
const s3 = 'https://bucket.s3.us-east-2.amazonaws.com/zotero/ABCD1234.zip?X-Amz-Signature=synthetic';
const headers = {'Authorization':'Basic synthetic', 'Content-Length':'12582912','Content-Type':'application/zip'};
const put = () => new Request('https://webdav.example.test/zotero/ABCD1234.zip', {method:'PUT', headers, body:new ReadableStream({pull(){throw new Error('File body must not be read');}},{highWaterMark:0}), duplex:'half'});
const signed = () => Response.json({url:s3, headers:{'Content-Type':'application/zip','Content-Length':'12582912'}});

test('307 signs metadata only without reading 12 MiB upload', async () => {
  const request=put(); let calls=0;
  const result=await handle(request,env,async (url,init) => {
    calls++; assert.equal(url,env.API_ORIGIN+'/_worker/upload'); assert.equal(init.method,'POST');
    assert.equal(init.redirect,'manual'); assert.equal(init.headers.Authorization,headers.Authorization);
    assert.equal(init.headers['X-WebDAV-Worker-Token'],env.WORKER_TOKEN);
    assert.deepEqual(JSON.parse(init.body),{path:'/zotero/ABCD1234.zip',contentType:'application/zip',contentLength:12582912});
    return signed();
  });
  assert.equal(result.status,307); assert.equal(result.headers.get('Location'),s3);
  assert.equal(request.bodyUsed,false); assert.equal(calls,1);
});
test('download preserves existing Lambda 307 without following it', async () => {
  let calls=0;
  const result=await handle(new Request('https://worker.test/zotero/ABCD1234.zip',{headers}),env,async (url,init)=>{
    calls++; assert.equal(init.redirect,'manual'); assert.equal(init.method,'GET');
    assert.equal(init.headers.has('X-WebDAV-Worker-Token'),false);
    return new Response(null,{status:307,headers:{Location:s3}});
  });
  assert.equal(result.status,307); assert.equal(calls,1);
});
test('PROPFIND is POST metadata to existing Lambda handlers', async () => {
  const req=new Request('https://worker.test/zotero/',{method:'PROPFIND',headers:{Authorization:'Basic synthetic',Depth:'1'},body:'<propfind/>'});
  const res=await handle(req,env,async(url,init)=>{
    assert.equal(url,env.API_ORIGIN+'/_worker/dav');assert.equal(init.method,'POST');
    assert.deepEqual(JSON.parse(init.body),{path:'/zotero/',method:'PROPFIND',depth:'1',body:'<propfind/>'});
    return new Response('<multistatus/>',{status:207});
  }); assert.equal(res.status,207);
});
test('missing credentials never touch origin or body',async()=>{
  const r=await handle(new Request('https://worker.test/zotero/'),env,()=>assert.fail('must not fetch'));
  assert.equal(r.status,401);
});
test('cannot use Worker as a generic proxy or internal endpoint',async()=>{
  for(const path of ['/_worker/upload','/other/file.zip','/zotero/file.zip?target=bad']) {
    assert.equal((await handle(new Request('https://worker.test'+path,{headers}),env,()=>assert.fail())).status,404);
  }
});
test('enforces declared limit before signing without claiming platform bypass',async()=>{
  assert.equal((await handle(put(),{...env,MAX_UPLOAD_BYTES:'6000000'},()=>assert.fail())).status,413);
});
test('rejects missing lengths and preconditions',async()=>{
  const noLength=new Request('https://worker.test/zotero/a.zip',{method:'PUT',headers:{Authorization:'Basic synthetic'}});
  assert.equal((await handle(noLength,env,()=>assert.fail())).status,411);
  const conditional=put();conditional.headers.set('If-Match','some-etag');
  assert.equal((await handle(conditional,env,()=>assert.fail())).status,412);
});
test('rejects untrusted signed destinations and never leaks upstream errors',async()=>{
  const r=await handle(put(),env,async()=>Response.json({url:'https://evil.test/zotero/a.zip?X-Amz-Signature=secret'}));
  assert.equal(r.status,502);assert.equal((await r.text()).includes('secret'),false);
});
test('enforces metadata cap for PROPFIND',async()=>{
  const r=await handle(new Request('https://worker.test/zotero/',{method:'PROPFIND',headers,body:'x'.repeat(65537)}),env,()=>assert.fail());
  assert.equal(r.status,413);
});
test('streaming sends body only to S3, waits for success, strips auth',async()=>{
  // Node shim tests wiring/backpressure, not Cloudflare native Content-Length behavior.
  globalThis.FixedLengthStream=class extends TransformStream { constructor(_length) { super(); } };
  const bytes=new Uint8Array(12*1024*1024).fill(17);
  const req=new Request('https://worker.test/zotero/ABCD1234.zip',{method:'PUT',headers,body:bytes});
  let calls=0;
  const r=await handle(req,{...env,UPLOAD_MODE:'stream'},async(url,init)=>{
    calls++;
    if(calls===1) {assert.equal(typeof init.body,'string');return signed();}
    assert.equal(url,s3);assert.equal(init.redirect,'manual');
    assert.deepEqual(init.headers,{'Content-Type':'application/zip'});
    const received=new Uint8Array(await new Response(init.body).arrayBuffer());
    assert.deepEqual(received,bytes);return new Response(null,{status:200});
  });assert.equal(r.status,200);assert.equal(calls,2);
});
test('S3 failure is not reported as upload success',async()=>{
  globalThis.FixedLengthStream=class extends TransformStream { constructor(_length) { super(); } };
  const req=new Request('https://worker.test/zotero/ABCD1234.zip',{method:'PUT',headers,body:new Uint8Array(12582912)});
  let calls=0;
  const r=await handle(req,{...env,UPLOAD_MODE:'stream'},async()=>++calls===1?signed():new Response('secret error',{status:403}));
  assert.equal(r.status,502);assert.equal((await r.text()).includes('secret'),false);
});
test('default export does not mistake Workers execution context for fetch',async()=>{
  const res=await worker.fetch(new Request('https://worker.test/zotero/'),env,{});
  assert.equal(res.status,401);
});
