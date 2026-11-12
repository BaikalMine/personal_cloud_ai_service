import { lstat,readdir,unlink,realpath } from 'node:fs/promises';
import { resolve,join,relative,sep } from 'node:path';
import { userIdentity } from './policy.mjs';

export async function inspectStorage(identity,expired=[],roots) {
  const allowed=roots??{native:join(identity.data,'dsh/attachments'),workspace:identity.workspace};
  const gone=new Set(expired),files=[];let count=0;
  for(const [scope,root]of Object.entries(allowed)){
    let physical;try{physical=await realpath(root);}catch(error){if(error.code==='ENOENT')continue;throw error;}
    if(physical!==resolve(root))throw new Error('storage root symlink');
    async function walk(directory){
      for(const entry of await readdir(directory,{withFileTypes:true})){
        if(++count>100000)throw new Error('storage file count exceeded');
        const path=join(directory,entry.name),part=relative(root,path);
        if(part==='..'||part.startsWith('..'+sep))throw new Error('storage boundary');
        let info;try{info=await lstat(path,{bigint:true});}catch(error){if(error.code==='ENOENT')continue;throw error;}
        if(info.isSymbolicLink())continue;
        if(info.isDirectory())await walk(path);
        else if(info.isFile()){
          const inode=String(info.dev)+':'+String(info.ino)+':'+String(info.birthtimeNs);
          if(gone.has(inode)){
            // This worker runs as the owning Unix UID, never as the manager.
            try{await unlink(path);}catch(error){if(error.code!=='ENOENT')throw error;}
          }else files.push({scope,path,inode,bytes:Number(info.size)});
        }
      }
    }
    await walk(root);
  }
  return files;
}

if(process.argv[1]?.endsWith('/storage-worker.mjs')){
  const identity=userIdentity(process.argv[2]);
  if(process.getuid()!==identity.uid)throw new Error('storage worker UID mismatch');
  let input='';for await(const chunk of process.stdin){input+=chunk;if(input.length>1048576)throw new Error('cleanup input too large');}
  const expired=JSON.parse(input||'[]');if(!Array.isArray(expired)||expired.some(x=>typeof x!=='string'||!/^\d+:\d+:\d+$/.test(x)))throw new Error('invalid inode set');
  console.log(JSON.stringify(await inspectStorage(identity,expired)));
}
