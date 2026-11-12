import { timingSafeEqual,createHash } from 'node:crypto';

export function userIdentity(value) {
  if(typeof value!=='string'||!/^\d+$/.test(value)||String(Number(value))!==value||Number(value)<1||Number(value)>60000)throw new Error('invalid user');
  const id=Number(value);
  return {id,uid:100000+id,gid:100000+id,bind:`127.91.${Math.floor(id/250)}.${id%250+1}`,data:`/data/users/u${id}`,workspace:`/workspace/users/u${id}`};
}
export function equalSecret(actual,expected) {
  const a=Buffer.from(actual??''),b=Buffer.from(expected??'');
  return b.length>=32&&a.length===b.length&&timingSafeEqual(a,b);
}
export function scopedIdentity(user,value) {
  if(typeof value!=='string'||!value||value.length>256)throw new Error('invalid session');
  return `u${user}-`+createHash('sha256').update(value).digest('hex');
}
export function publicHeaders(headers,host,cookie) {
  const result={};
  for(const [key,value]of Object.entries(headers)){
    if(/^(authorization|cookie|x-harness-user|x-forwarded-.+|x-ai-.+|idempotency-key|host)$/i.test(key))continue;
    result[key]=value;
  }
  result.host=host;result.cookie=cookie;return result;
}
