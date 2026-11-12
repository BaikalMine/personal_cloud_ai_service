import { createServer,request } from 'node:http';
import { spawn } from 'node:child_process';
import { mkdir,chown,chmod,lstat,readdir,realpath } from 'node:fs/promises';
import { randomBytes } from 'node:crypto';
import { resolve,relative,isAbsolute,join } from 'node:path';
import { userIdentity,equalSecret,scopedIdentity,publicHeaders } from './policy.mjs';
import { UploadRetention } from './retention.mjs';

const key=process.env.HARNESS_TENANT_KEY,origin=new URL(process.env.HARNESS_PUBLIC_URL),gateway=new URL(process.env.HARNESS_GATEWAY_URL);
if(process.getuid()!==0||!key||key.length<32||gateway.protocol!=='http:')throw new Error('Invalid user manager configuration');
await mkdir('/data/manager',{recursive:true,mode:0o700});
await chmod('/data/manager',0o700);
await mkdir('/data/users',{recursive:true,mode:0o755});await mkdir('/workspace/users',{recursive:true,mode:0o755});
const retention=new UploadRetention('/data/manager/uploads.sqlite');
const tenants=new Map(),maxUsers=Number(process.env.HARNESS_MAX_ACTIVE_USERS||16);
const secret=()=>randomBytes(32).toString('hex');
const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const fail=(res,status=503)=>{res.writeHead(status,{'Content-Type':'text/plain; charset=utf-8','Cache-Control':'no-store'}).end(status===403?'Доступ к Harness закрыт.':'Harness временно недоступен. Повторите запрос позже.');};

async function checkAccess(id){
  const response=await fetch(new URL(`/internal/harness-users/${id}/access`,gateway),{headers:{Authorization:`Bearer ${key}`},signal:AbortSignal.timeout(10000)});
  return response.ok&&(await response.json()).allowed===true;
}
function stop(tenant){
  tenant.cookie=null;
  for(const socket of tenant.sockets??[])socket.destroy();
  if(tenant.child){try{process.kill(-tenant.child.pid,'SIGTERM');}catch{};const pid=tenant.child.pid;setTimeout(()=>{try{process.kill(-pid,'SIGKILL');}catch{}},5000).unref();}
  tenants.delete(tenant.identity.id);
}
async function ensure(id){
  if(!await checkAccess(id))throw Object.assign(new Error('access denied'),{status:403});
  let tenant=tenants.get(id);
  if(tenant){await tenant.ready;tenant.used=Date.now();return tenant;}
  if(tenants.size>=maxUsers)throw new Error('user capacity');
  const identity=userIdentity(String(id));
  tenant={identity,modelKey:secret(),bootstrapKey:secret(),cookie:null,sockets:new Set(),used:Date.now(),calls:0};
  tenants.set(id,tenant);
  tenant.ready=(async()=>{
    for(const root of [identity.data,identity.workspace]){
      await mkdir(root,{recursive:true,mode:0o700});
      const info=await lstat(root);if(!info.isDirectory()||info.isSymbolicLink())throw new Error('user storage root invalid');
      await chown(root,identity.uid,identity.gid);
      await chmod(root,0o700);
    }
    const env={PATH:'/usr/local/bin:/usr/bin:/bin',LANG:'C.UTF-8',HOME:identity.workspace,DSH_HOME:identity.data+'/dsh',XDG_CACHE_HOME:identity.data+'/cache',
      HARNESS_PUBLIC_URL:origin.href,HARNESS_TRUSTED_HOSTS:origin.host,HARNESS_BIND_HOST:identity.bind,HARNESS_WORKSPACE:identity.workspace,
      HARNESS_BOOTSTRAP_KEY:tenant.bootstrapKey,HARNESS_MODEL_KEY:tenant.modelKey,HARNESS_MODEL_URL:`http://127.0.0.1:8098/model/u${id}/v1`,
      HARNESS_MODELS:process.env.HARNESS_MODELS,HARNESS_TASK_RETENTION:'true',HARNESS_PYTHON_ENV:'false',HARNESS_REA_ENABLED:'false',
      HARNESS_UPLOAD_RETENTION_URL:`http://127.0.0.1:8098/retention/u${id}`,HARNESS_USER_MODE:'true',PIP_REQUIRE_VIRTUALENV:'true',NARB_DISABLE_NATIVE_CACHE:'1'};
    tenant.child=spawn(process.execPath,['/opt/harness/users-entry.mjs'],{uid:identity.uid,gid:identity.gid,cwd:identity.workspace,env,detached:true,stdio:process.env.HARNESS_TEST_DIAGNOSTICS==='true'?['ignore','inherit','inherit']:['ignore','ignore','ignore']});
    tenant.child.on('exit',()=>{if(tenants.get(id)===tenant)tenants.delete(id);for(const socket of tenant.sockets)socket.destroy();});
    tenant.child.on('error',()=>{if(tenants.get(id)===tenant)tenants.delete(id);});
    for(let attempt=0;attempt<100;attempt++){
      if(tenant.child.exitCode!==null)throw new Error('user runtime exited');
      try{
        const response=await fetch(`http://${identity.bind}:3082/session?host=${encodeURIComponent(origin.host)}`,{headers:{Authorization:`Bearer ${tenant.bootstrapKey}`},signal:AbortSignal.timeout(1000)});
        if(response.ok){const result=await response.json();if(typeof result.cookie==='string'&&result.cookie.startsWith('dsh-auth-')){tenant.cookie=result.cookie.split(';')[0];return;}}
      }catch{}
      await delay(200);
    }
    throw new Error('user runtime timeout');
  })().catch(error=>{stop(tenant);throw error;});
  await tenant.ready;return tenant;
}

async function readJSON(req,limit){let bytes=0,text='';for await(const chunk of req){bytes+=chunk.length;if(bytes>limit)throw new Error('request too large');text+=chunk;}return JSON.parse(text);}
async function registerUpload(tenant,body){
  const root=tenant.identity.workspace,path=resolve(body.path??'');
  const part=relative(root,path);if(!part||part==='..'||part.startsWith('../')||isAbsolute(part))throw new Error('outside workspace');
  if(await realpath(path)!==path)throw new Error('upload symlink');
  const info=await lstat(path,{bigint:true});if(!info.isFile()||Number(info.uid)!==tenant.identity.uid)throw new Error('upload owner');
  retention.register(tenant.identity.id,`${info.dev}:${info.ino}:${info.birthtimeNs}`);
}
function modelProxy(req,res,tenant,path){
  const target=new URL(`/internal/harness-users/${tenant.identity.id}/${path}`,gateway);
  const headers={...req.headers,host:target.host,authorization:`Bearer ${key}`};
  delete headers.cookie;delete headers['x-harness-user'];
  if(headers['x-ai-harness-session'])headers['x-ai-harness-session']=scopedIdentity(tenant.identity.id,headers['x-ai-harness-session']);
  const forward=body=>{
    if(body){headers['content-length']=Buffer.byteLength(body);delete headers['transfer-encoding'];}
    tenant.calls++;
    const upstream=request(target,{method:req.method,headers},response=>{res.writeHead(response.statusCode,response.headers);response.pipe(res);});
    const finish=()=>{tenant.calls=Math.max(0,tenant.calls-1);tenant.used=Date.now();};let finished=false;
    const done=()=>{if(!finished){finished=true;finish();}};
    res.on('close',()=>{if(!res.writableFinished)upstream.destroy();done();});upstream.on('error',()=>{if(!res.headersSent)fail(res);else res.destroy();done();});res.on('finish',done);
    if(body)upstream.end(body);else req.pipe(upstream);
  };
  if(path==='harness/task')return readJSON(req,2048).then(body=>{if(!['begin','touch','end'].includes(body.action))throw new Error('invalid task');body.taskId=scopedIdentity(tenant.identity.id,body.taskId);body.sessionId=scopedIdentity(tenant.identity.id,body.sessionId);forward(JSON.stringify(body));});
  forward();
}
function webProxy(req,res,tenant){
  const headers=publicHeaders(req.headers,origin.host,tenant.cookie);
  const upstream=request({hostname:tenant.identity.bind,port:3080,path:req.url,method:req.method,headers,agent:false},response=>{
    const output={...response.headers,'cache-control':'no-store'};delete output['set-cookie'];res.writeHead(response.statusCode,output);response.pipe(res);
  });
  req.on('aborted',()=>upstream.destroy());res.on('close',()=>{if(!res.writableFinished)upstream.destroy();});
  upstream.on('error',error=>{if(process.env.HARNESS_TEST_DIAGNOSTICS==='true')console.error('user proxy',error.code);if(!res.headersSent)fail(res);else res.destroy();});req.pipe(upstream);
}
async function runningTasks(tenant){
  await tenant.ready;
  const result=await new Promise((resolve,reject)=>{
    const upstream=request({hostname:tenant.identity.bind,port:3080,path:'/api/session/list',method:'POST',agent:false,headers:{Host:origin.host,Origin:origin.origin,Cookie:tenant.cookie,'Content-Type':'application/json'}},response=>{
      let text='';response.setEncoding('utf8');response.on('data',chunk=>{text+=chunk;if(text.length>4*1024*1024)upstream.destroy(new Error('session inventory too large'));});response.on('end',()=>{try{if(response.statusCode!==200)throw new Error('idle state unavailable');resolve(JSON.parse(text));}catch(error){reject(error);}});response.on('error',reject);
    });
    upstream.setTimeout(10000,()=>upstream.destroy(new Error('idle state timeout')));upstream.on('error',reject);
    upstream.end(JSON.stringify({type:'client-request',rpcId:secret(),method:'session/list',payload:{args:{_request:{}}}}));
  });
  if(!result.result?.ok||!Array.isArray(result.result.value.items))throw new Error('idle state unknown');
  return result.result.value.items.filter(x=>x.running).length+tenant.calls;
}
const server=createServer(async(req,res)=>{
  try{
    const url=new URL(req.url,'http://manager.invalid');
    if(url.pathname==='/healthz'&&req.method==='GET'){res.writeHead(200).end();return;}
    const model=url.pathname.match(/^\/model\/u([1-9]\d*)\/(v1\/models|v1\/chat\/completions|harness\/task)$/);
    const registration=url.pathname.match(/^\/retention\/u([1-9]\d*)$/);
    if(model||registration){
      const tenant=tenants.get(Number((model??registration)[1]));
      if(!tenant||!equalSecret(req.headers.authorization,`Bearer ${tenant.modelKey}`)||!await checkAccess(tenant.identity.id)){fail(res,403);return;}
      if(registration){if(req.method!=='POST'){fail(res,403);return;}await registerUpload(tenant,await readJSON(req,8192));res.writeHead(200,{'Content-Type':'application/json'}).end('{"ok":true}');return;}
      const path=model[2];if(url.search||!((path==='v1/models'&&req.method==='GET')||(path!=='v1/models'&&req.method==='POST'))){fail(res,403);return;}
      tenant.used=Date.now();await modelProxy(req,res,tenant,path);return;
    }
    if(!equalSecret(req.headers.authorization,`Bearer ${key}`)){fail(res,403);return;}
    if(url.pathname==='/control/idle'&&req.method==='GET'){
      let running=0;
      for(const tenant of tenants.values()){
        running+=await runningTasks(tenant);
      }
      res.writeHead(200,{'Content-Type':'application/json'}).end(JSON.stringify({running}));return;
    }
    const identity=userIdentity(req.headers['x-harness-user']);
    const tenant=await ensure(identity.id);webProxy(req,res,tenant);
  }catch(error){if(process.env.HARNESS_TEST_DIAGNOSTICS==='true')console.error('user manager',error);if(!res.headersSent)fail(res,error.status??503);else res.destroy();}
});
server.requestTimeout=15*60*1000;server.headersTimeout=10000;
server.on('upgrade',async(req,socket,head)=>{
  try{
    if(!equalSecret(req.headers.authorization,`Bearer ${key}`))throw new Error('unauthorized');
    const identity=userIdentity(req.headers['x-harness-user']),tenant=await ensure(identity.id);
    const upstream=request({hostname:identity.bind,port:3080,path:req.url,method:'GET',headers:publicHeaders(req.headers,origin.host,tenant.cookie)});
    tenant.sockets.add(socket);socket.on('close',()=>{tenant.sockets.delete(socket);upstream.destroy();});
    upstream.on('upgrade',(response,peer,data)=>{
      const headers=Object.entries(response.headers).filter(([key])=>key!=='set-cookie').map(([key,value])=>`${key}: ${value}`).join('\r\n');
      socket.write(`HTTP/1.1 101 Switching Protocols\r\n${headers}\r\n\r\n`);if(data.length)socket.write(data);if(head.length)peer.write(head);peer.pipe(socket);socket.pipe(peer);peer.on('error',()=>socket.destroy());
    });
    upstream.on('response',()=>socket.destroy());upstream.on('error',()=>socket.destroy());upstream.end();
  }catch{socket.destroy();}
});

async function scan(identity){
  const expired=retention.expired(identity.id);
  const child=spawn(process.execPath,['/opt/harness-users/storage-worker.mjs',String(identity.id)],{uid:identity.uid,gid:identity.gid,stdio:['pipe','pipe','ignore'],env:{PATH:'/usr/local/bin:/usr/bin:/bin'}});
  const text=await new Promise((resolve,reject)=>{let output='';const timer=setTimeout(()=>{child.kill('SIGKILL');reject(new Error('cleanup timed out'));},20000);child.on('error',reject);child.stdout.on('data',chunk=>{output+=chunk;if(output.length>16777216){child.kill();reject(new Error('cleanup output too large'));}});child.on('exit',code=>{clearTimeout(timer);code===0?resolve(output):reject(new Error('cleanup failed'));});child.stdin.end(JSON.stringify(expired));});
  for(const file of JSON.parse(text))if(file.scope==='native')retention.register(identity.id,file.inode);
}
let scanning=false;
const sweep=setInterval(async()=>{
  if(scanning)return;scanning=true;
  try{
    for(const entry of await readdir('/data/users')){
      if(!/^u[1-9]\d*$/.test(entry))continue;
      try{
        const identity=userIdentity(entry.slice(1));
        const tenant=tenants.get(identity.id);
        if(tenant&&!await checkAccess(identity.id))stop(tenant);
        else if(tenant&&Date.now()-tenant.used>3600000&&!await runningTasks(tenant))stop(tenant);
        await scan(identity);
      }catch{console.error('Harness user retention check failed');}
    }
  }catch{console.error('Harness file retention check failed');}finally{scanning=false;}
},30000);
server.listen(8098,'0.0.0.0');
for(const event of ['SIGTERM','SIGINT'])process.on(event,()=>{clearInterval(sweep);server.close();for(const tenant of tenants.values())stop(tenant);setTimeout(()=>{retention.close();process.exit(0);},6000).unref();});
