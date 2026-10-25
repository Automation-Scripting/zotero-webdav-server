"""Optional isolated AWS probe, NOT deployed. Requires boto3, Lambda environment.
Only disposable bucket and keys. No logging of events, credentials or signed URLs.
This handler deliberately does not implement WebDAV: measures ingress separately.
"""
import base64, hmac, json, os, re
import boto3
from botocore.config import Config

def handler(event, context):
    bucket = os.environ['POC_BUCKET']
    if not bucket.endswith('-webdav-poc'): raise ValueError('Not a disposable bucket')
    headers = {k.lower():v for k,v in event.get('headers', {}).items()}
    expected = 'Basic ' + base64.b64encode((os.environ['POC_USER']+':'+os.environ['POC_PASSWORD']).encode()).decode()
    if not hmac.compare_digest(headers.get('authorization',''), expected):
        return {'statusCode':401, 'headers':{'WWW-Authenticate':'Basic realm="POC"'},'body':''}
    method = event.get('requestContext',{}).get('http',{}).get('method') or event.get('httpMethod')
    path = event.get('rawPath') or event.get('path','')
    if method not in ['GET','PUT'] or not re.fullmatch(r'/zotero/probe-[0-9]+\.zip',path):
        return {'statusCode':405, 'body':''}
    # This only runs AFTER the managed ingress has built the invocation event.
    print(json.dumps({'request_id':context.aws_request_id, 'method':method,
                      'event_body_chars':len(event.get('body','')), 'base64':event.get('isBase64Encoded',False)}))
    client = boto3.client('s3',config=Config(signature_version='s3v4',request_checksum_calculation='when_required'))
    params={'Bucket':bucket,'Key':'webdav-poc'+path}
    if method=='PUT': params['ContentType']=headers.get('content-type','application/zip')
    location=client.generate_presigned_url('put_object' if method=='PUT' else 'get_object',Params=params,ExpiresIn=120,HttpMethod=method)
    return {'statusCode':307,'headers':{'Location':location,'Cache-Control':'no-store'},'body':''}
