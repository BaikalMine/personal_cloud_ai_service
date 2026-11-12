// Run only inside an ephemeral user-manager container; no model/GPU is contacted.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {randomUUID} from 'node:crypto';
import {writeFile,chown,readFile} from 'node:fs/promises';
import {userIdentity} from './policy.mjs';
import {networkInterfaces} from 'node:os';
const exec=promisify(execFile),key=process.env.HARNESS_TENANT_KEY;
const denied=new Set(),modelCalls=[];
const fixture=createServer(async(req,res)=>{
 assert.equal(req.headers.authorization,'Bearer '+key);
 const match=req.url.match(/^\/internal\/harness-users\/(\d+)\/(.*)$/);
 if(!match||denied.has(Number(match[1]))){res.writeHead(403).end();return;}
 res.setHeader('Content-Type','application/json');
 if(match[2]==='access'){res.end('{"allowed":true}');return;}
 let raw='';for await(const chunk of req)raw+=chunk;
 modelCalls.push({user:Number(match[1]),path:match[2],body:raw?JSON.parse(raw):null,session:req.headers['x-ai-harness-session']});
 res.end(JSON.stringify(match[2]==='v1/models'?{data:[]}:{ok:true}));
});
await new Promise(resolve=>fixture.listen(8099,'0.0.0.0',resolve));
const base='http://127.0.0.1:8098',origin=new URL(process.env.HARNESS_PUBLIC_URL).origin;
function headers(id){return {Authorization:'Bearer '+key,'X-Harness-User':String(id),Origin:origin,Cookie:'dsh-auth-wrong=must-not-select-another-user'};}
async function rpc(id,method,request){
 const response=await fetch(base+'/api/'+method,{method:'POST',headers:{...headers(id),'Content-Type':'application/json'},body:JSON.stringify({type:'client-request',rpcId:randomUUID(),method,payload:{args:{request}}})});
 assert.equal(response.status,200,await response.clone().text());return (await response.json()).result;
}
try{
 for(const id of [101,102]){
  const response=await fetch(base+'/',{headers:headers(id),redirect:'manual'});assert.equal(response.status,200,await response.clone().text());
  const result=await rpc(id,'session/create',{sessionId:'private-session-'+id,cwd:userIdentity(String(id)).workspace});assert.equal(result.ok,true,JSON.stringify(result));
  const title=await rpc(id,'session/rename',{sessionId:'private-session-'+id,title:'Private QA '+id});assert.equal(title.ok,true,JSON.stringify(title));
 }
 const foreign=await rpc(102,'session/rename',{sessionId:'private-session-101',title:'must-not-work'});assert.notEqual(foreign?.ok,true);
 const own=await rpc(101,'session/rename',{sessionId:'private-session-101',title:'Still owned'});assert.equal(own.ok,true);
 const idle=await fetch(base+'/control/idle',{headers:{Authorization:'Bearer '+key}});assert.equal(idle.status,200);assert.equal((await idle.json()).running,0);
 const a=userIdentity('101'),b=userIdentity('102');
 for(const user of [a,b]){const path=user.workspace+'/private.txt';await writeFile(path,'private '+user.id,{mode:0o600});await chown(path,user.uid,user.gid);}
 await assert.rejects(exec('/bin/cat',[b.workspace+'/private.txt'],{uid:a.uid,gid:a.gid}));
 await assert.rejects(exec('/bin/cat',['/proc/1/environ'],{uid:a.uid,gid:a.gid}));
 assert.equal((await exec('/bin/cat',[a.workspace+'/private.txt'],{uid:a.uid,gid:a.gid})).stdout,'private 101');
 const status=(await exec('/bin/cat',['/proc/self/status'],{uid:a.uid,gid:a.gid})).stdout;assert.match(status,/CapEff:\s+0000000000000000/);
 const privateIP=Object.values(networkInterfaces()).flat().find(x=>x.family==='IPv4'&&!x.internal).address;
 await assert.rejects(exec('/usr/local/bin/node',['-e',`fetch('http://${privateIP}:8099',{signal:AbortSignal.timeout(1000)}).then(()=>process.exit(0)).catch(()=>process.exit(1))`],{uid:a.uid,gid:a.gid}));
 const native=await fetch(base+'/api/session/uploadFileBinary?'+new URLSearchParams({sessionId:'private-session-101',name:'native.txt'}),{method:'POST',headers:{...headers(101),'Content-Type':'application/octet-stream'},body:'native upload'});assert.equal(native.status,200);assert.equal((await native.json()).ok,true);
 const upload=await fetch(base+'/api/gateway-files/upload?'+new URLSearchParams({sessionId:'private-session-101',path:a.workspace,name:'upload.txt',bytes:'6'}),{method:'POST',headers:{...headers(101),'Content-Type':'application/octet-stream'},body:'upload'});assert.equal(upload.status,200);const result=await upload.json();assert.equal(result.ok,true,JSON.stringify(result));
 const {DatabaseSync}=await import('node:sqlite');const db=new DatabaseSync('/data/manager/uploads.sqlite');assert(db.prepare('SELECT count(*) AS n FROM uploads WHERE user=101').get().n>=1);db.close();
 const deniedDownload=await fetch(base+'/api/gateway-files/download?'+new URLSearchParams({sessionId:'private-session-102',path:a.workspace+'/upload.txt'}),{headers:headers(102)});assert.equal(deniedDownload.status,400);
 // Read only the synthetic process's own model key, never production environment data.
 const profile=await readFile(a.data+'/dsh/profiles/web/cordis.patch.yml','utf8');assert(!profile.includes(key));
 const pids=(await exec('/bin/sh',['-c',"for p in /proc/[0-9]*; do test -r \"$p/environ\" && tr '\\0' '\\n' < \"$p/environ\" 2>/dev/null | sed -n 's/^HARNESS_MODEL_KEY=//p'; done; exit 0"],{uid:a.uid,gid:a.gid})).stdout.trim().split('\n').filter(Boolean);
 const modelKey=pids[0];assert(modelKey&&modelKey!==key);
 for(const action of ['begin','touch','end']){const response=await fetch(base+'/model/u101/harness/task',{method:'POST',headers:{Authorization:'Bearer '+modelKey,'Content-Type':'application/json'},body:JSON.stringify({action,taskId:'same-task',sessionId:'same-session'})});assert.equal(response.status,200);}
 assert.equal(modelCalls.length,3);for(const call of modelCalls){assert.equal(call.user,101);assert.match(call.body.sessionId,/^u101-/);assert.match(call.body.taskId,/^u101-/);}
 const impersonation=await fetch(base+'/model/u102/v1/models',{headers:{Authorization:'Bearer '+modelKey}});assert.equal(impersonation.status,403);
 await new Promise(resolve=>setTimeout(resolve,31000));
 const ttlDB=new DatabaseSync('/data/manager/uploads.sqlite');assert(ttlDB.prepare('SELECT count(*) AS n FROM uploads WHERE user=101').get().n>=2);
 ttlDB.prepare('UPDATE uploads SET expires=0 WHERE user=101').run();ttlDB.close();
 await new Promise(resolve=>setTimeout(resolve,31000));
 await assert.rejects(readFile(a.workspace+'/upload.txt'),{code:'ENOENT'});
 assert.equal((await rpc(101,'session/rename',{sessionId:'private-session-101',title:'History survives expired uploads'})).ok,true);
 denied.add(101);const revoked=await fetch(base+'/',{headers:headers(101)});assert.equal(revoked.status,403);
 const revokedModel=await fetch(base+'/model/u101/v1/models',{headers:{Authorization:'Bearer '+modelKey}});assert.equal(revokedModel.status,403);
 console.log('PASS: two real Harness processes, private chats/files, native/workspace upload, durable TTL, UID/capability isolation, scoped model leases, token isolation and immediate revocation. No GPU/model calls.');
}finally{fixture.close();fixture.closeAllConnections();}
