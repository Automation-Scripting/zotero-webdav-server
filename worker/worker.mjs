// No request/response logging: headers and signed URLs are credentials.
const METHODS = new Set(['OPTIONS', 'PROPFIND', 'MKCOL', 'HEAD', 'GET', 'PUT', 'DELETE']);
const META_LIMIT = 64 * 1024;
const reply = (status, text = '') => new Response(text || null, {
  status, headers: {'Cache-Control': 'no-store', 'Content-Type': 'text/plain; charset=utf-8'}
});

async function smallText(body, limit) {
  if (!body) return '';
  const reader = body.getReader();
  let length = 0;
  const chunks = [];
  try {
    for (;;) {
      const {done, value} = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > limit) { await reader.cancel(); throw new RangeError('Metadata too large'); }
      chunks.push(value);
    }
  } finally { reader.releaseLock(); }
  const bytes = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
  return new TextDecoder('utf-8', {fatal:true}).decode(bytes);
}

function checkedS3URL(value, env) {
  const url = new URL(value);
  if (url.protocol !== 'https:' || url.host !== env.S3_HOST || url.username || url.password || url.hash
      || !url.pathname.startsWith(`/${env.WEBDAV_PREFIX || 'zotero'}/`)
      || !url.searchParams.has('X-Amz-Signature')) throw new Error('Invalid signed destination');
  return url;
}

async function run(request, env, fetcher) {
  if (!METHODS.has(request.method)) return reply(405);
  const incoming = new URL(request.url);
  const root = '/' + (env.WEBDAV_PREFIX || 'zotero');
  if (incoming.search || (incoming.pathname !== root && !incoming.pathname.startsWith(root + '/'))) return reply(404);
  if (!env.WORKER_TOKEN || env.WORKER_TOKEN.length < 32) return reply(503);
  const origin = new URL(env.API_ORIGIN);
  if (origin.protocol !== 'https:' || origin.username || origin.password || origin.search || origin.hash) return reply(503);
  const endpoint = path => origin.href.replace(/\/$/, '') + path;
  const authorization = request.headers.get('Authorization');
  if (!authorization) return new Response(null, {status:401, headers:{'WWW-Authenticate':'Basic realm="Zotero WebDAV", charset="UTF-8"','Cache-Control':'no-store'}});
  const bridge = async (path, metadata) => fetcher(endpoint(path), {
    method:'POST', redirect:'manual', signal:request.signal,
    headers:{'Authorization':authorization, 'X-WebDAV-Worker-Token':env.WORKER_TOKEN, 'Content-Type':'application/json'},
    body:JSON.stringify(metadata)
  });
  if (request.method === 'PUT') {
    // Fail closed on unknown lengths; never buffer the file to determine its size.
    const rawLength = request.headers.get('Content-Length');
    if (rawLength === null || !/^\d+$/.test(rawLength)) return reply(411, 'Content-Length required.\n');
    const length = Number(rawLength);
    const limit = Number(env.MAX_UPLOAD_BYTES || 100000000);
    if (!Number.isSafeInteger(length) || length > limit) return reply(413, 'Upload exceeds configured limit.\n');
    if (request.headers.has('Content-Encoding') || request.headers.has('Content-Range')) return reply(415);
    // Do not silently drop preconditions and change write semantics.
    if (request.headers.has('If-Match') || request.headers.has('If-None-Match')) return reply(412);
    if (env.UPLOAD_MODE === 'redirect' && !request.headers.has('Content-Type')) return reply(415, 'Content-Type required for redirected uploads.\n');
    const contentType = request.headers.get('Content-Type') || 'application/octet-stream';
    const signedResponse = await bridge('/_worker/upload', {path:incoming.pathname, contentType, contentLength:length});
    if (signedResponse.status !== 200) return safeBackendResponse(signedResponse, env, request.method);
    const signed = JSON.parse(await smallText(signedResponse.body, META_LIMIT));
    const destination = checkedS3URL(signed.url, env);
    if (signed.headers?.['Content-Type'] !== contentType || signed.headers?.['Content-Length'] !== String(length)) return reply(502);
    if (env.UPLOAD_MODE === 'redirect') {
      // Does not read request.body. Platform ingress and client replay still need real tests.
      return new Response(null, {status:307, headers:{Location:destination.href, 'Cache-Control':'no-store'}});
    }
    if (env.UPLOAD_MODE !== 'stream') return reply(503);
    // FixedLengthStream makes Workers emit Content-Length, not chunked encoding.
    // Never copy Basic auth, cookies, worker token, or arbitrary client headers to S3.
    const abort = new AbortController();
    const onAbort = () => abort.abort();
    request.signal.addEventListener('abort', onAbort, {once:true});
    if (request.signal.aborted) abort.abort();
    let piping;
    try {
      let body = new Uint8Array(0);
      if (length > 0) {
        if (!request.body) return reply(400);
        const fixed = new FixedLengthStream(length);
        piping = request.body.pipeTo(fixed.writable, {signal:abort.signal});
        // Attach rejection immediately; propagate via abort without logging payload/URL.
        piping.catch(() => abort.abort());
        body = fixed.readable;
      }
      const uploaded = await fetcher(destination.href, {
        method:'PUT', body, headers:{'Content-Type':contentType},
        redirect:'manual', signal:abort.signal, duplex:'half'
      });
      if (uploaded.status !== 200) {
        abort.abort();
        await uploaded.body?.cancel();
        return reply(uploaded.status === 429 || uploaded.status >= 500 ? 503 : 502, 'S3 upload failed.\n');
      }
      if (piping) await piping;
      await uploaded.body?.cancel();
      // S3 PutObject returns 200 for create/overwrite; Zotero accepts it.
      return reply(200);
    } finally {
      abort.abort();
      request.signal.removeEventListener('abort', onAbort);
    }
  }
  if (request.method === 'PROPFIND' || request.method === 'MKCOL') {
    const body = await smallText(request.body, META_LIMIT);
    return safeBackendResponse(await bridge('/_worker/dav', {
      path:incoming.pathname, method:request.method, depth:request.headers.get('Depth') || '', body
    }), env, request.method);
  }
  // GET's existing Lambda 307 must be returned to Zotero, never followed by the Worker.
  const headers = new Headers({Authorization:authorization});
  for (const name of ['Range','If-None-Match','If-Modified-Since','If-Match']) {
    if (request.headers.has(name)) headers.set(name, request.headers.get(name));
  }
  return safeBackendResponse(await fetcher(endpoint(incoming.pathname), {
    method:request.method, headers, redirect:'manual', signal:request.signal
  }), env, request.method);
}

function safeBackendResponse(response, env, method) {
  const headers = new Headers(response.headers);
  headers.set('Cache-Control', 'no-store');
  headers.delete('Set-Cookie');
  if (response.status >= 300 && response.status < 400) {
    if (method !== 'GET' || response.status !== 307) return reply(502);
    checkedS3URL(headers.get('Location'), env);
  }
  return new Response(method === 'HEAD' ? null : response.body, {status:response.status, headers});
}

export async function handle(request, env, fetcher = fetch) {
  try { return await run(request, env, fetcher); }
  catch (error) {
    return reply(error instanceof RangeError ? 413 : 502, 'WebDAV upstream request failed.\n');
  }
}
export default {fetch(request, env) { return handle(request, env); }};
