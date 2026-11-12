import assert from 'node:assert/strict';
import { test } from 'node:test';
import { uploadWorkspaceFile } from './workspace-upload-client.mjs';

function fixture() {
  const xhr={upload:{},open(method,url){this.method=method;this.url=url;},setRequestHeader(name,value){this.header=[name,value];},send(body){this.body=body;},abort(){this.onabort();}};
  return xhr;
}
test('browser sends the original Blob and reports byte progress without arrayBuffer or base64',async()=>{
  const xhr=fixture(),file=new Blob(['data']),progress=[];
  file.arrayBuffer=()=>{throw new Error('must not buffer');};
  const pending=uploadWorkspaceFile({url:new URL('https://local.example/harness/api/gateway-files/upload'),file,createXHR:()=>xhr,onProgress:(...args)=>progress.push(args)});
  assert.equal(xhr.body,file);assert.deepEqual(xhr.header,['Content-Type','application/octet-stream']);
  xhr.upload.onprogress({loaded:2,total:4,lengthComputable:true});
  xhr.status=200;xhr.responseText=JSON.stringify({ok:true,value:{bytes:4}});xhr.onload();
  assert.deepEqual(await pending,{bytes:4});assert.deepEqual(progress,[[2,4]]);
});
test('browser surfaces limit and conflict errors and cancels transport',async()=>{
  for(const [status,body,code] of [[413,'HTML','stream_upload_too_large'],[400,'{"ok":false,"error":{"code":"exists"}}','exists'],[502,'HTML','unavailable']]){
    const xhr=fixture(),pending=uploadWorkspaceFile({url:new URL('https://local.example/upload'),file:new Blob(['x']),createXHR:()=>xhr});
    xhr.status=status;xhr.responseText=body;xhr.onload();await assert.rejects(pending,e=>e.code===code);
  }
  const xhr=fixture(),controller=new AbortController();
  const pending=uploadWorkspaceFile({url:new URL('https://local.example/upload'),file:new Blob(['x']),createXHR:()=>xhr,signal:controller.signal});
  controller.abort();await assert.rejects(pending,e=>e.name==='AbortError');
});
