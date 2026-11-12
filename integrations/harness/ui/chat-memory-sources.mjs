const allowed=new Set(['file','chapter','line_from','line_to','anchor','sha256']);
const invalid=()=>{throw new Error('invalid_sources');};
const bounded=(value,bytes)=>typeof value==='string'&&!!value.trim()&&Buffer.byteLength(value)<=bytes;
const line=value=>Number.isSafeInteger(value)&&value>=1&&value<=1_000_000_000;

// References are metadata only. This module never resolves paths or reads files.
export function normalizeMemorySources(value, { workspaceRoot } = {}) {
  if(!Array.isArray(value)||value.length>4)invalid();
  const root=typeof workspaceRoot==='string'&&workspaceRoot.startsWith('/')?workspaceRoot.replace(/\/+$/,''):'';
  const prefix=root?root+'/':null;
  const result=[];
  for(const input of value){
    if(!input||typeof input!=='object'||Array.isArray(input)||Object.keys(input).some(key=>!allowed.has(key)))invalid();
    const path=input.file;
    const file=prefix&&typeof path==='string'&&path.startsWith(prefix)?path.slice(prefix.length):path;
    if(!bounded(file,512)||/[\p{Cc}\\:]/u.test(file)||file.split('/').some(part=>!part||part==='.'||part==='..'))invalid();
    if(input.chapter!==undefined&&(!bounded(input.chapter,128)||/\p{Cc}/u.test(input.chapter)))invalid();
    if(input.line_from!==undefined&&!line(input.line_from))invalid();
    if(input.line_to!==undefined&&(!line(input.line_to)||input.line_from===undefined||input.line_to<input.line_from))invalid();
    if(input.anchor!==undefined&&!bounded(input.anchor,512))invalid();
    if(input.sha256!==undefined&&(typeof input.sha256!=='string'||!/^[0-9a-f]{64}$/.test(input.sha256)))invalid();
    const source={file};
    for(const key of ['chapter','line_from','line_to','anchor','sha256'])if(input[key]!==undefined)source[key]=input[key];
    result.push(source);
  }
  if(Buffer.byteLength(JSON.stringify(result))>4096)invalid();
  return result;
}
