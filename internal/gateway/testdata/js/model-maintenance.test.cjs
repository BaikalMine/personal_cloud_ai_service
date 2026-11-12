const test=require('node:test'),assert=require('node:assert/strict');
const {create}=require('../../static/model-maintenance.js');
test('checks are sequential, deduplicated and never install or delete',async()=>{
 const calls=[];let active=0;
 const c=create({check:async token=>{assert.equal(active++,0);calls.push(token);await Promise.resolve();active--;return {update:token==='b'};}});
 c.start('loras',['a','a','b']);await c.run();
 assert.deepEqual(calls,['a','b']);assert.equal(c.snapshot().done,2);assert.equal(c.snapshot().updates,1);assert.equal(c.snapshot().status,'completed');
});
test('failed request pauses without losing its place or retrying automatically',async()=>{
 let calls=0,saved;
 const c=create({check:async()=>{calls++;throw new Error('rate limited');},save:s=>saved=s});
 c.start('loras',['a','b']);await c.run();
 assert.equal(calls,1);assert.equal(saved.done,0);assert.equal(saved.status,'paused');
 const resumed=create({initial:saved,check:async()=>({update:false})});
 assert.equal(resumed.snapshot().status,'paused');await resumed.run();assert.equal(resumed.snapshot().done,2);
});
test('page departure finishes only the current check and persists the remaining queue',async()=>{
 let resolve,saved;
 const c=create({check:()=>new Promise(r=>resolve=r),save:s=>saved=s});
 c.start('loras',['a','b']);const run=c.run();c.pause();resolve({update:true});await run;
 assert.equal(saved.status,'paused');assert.equal(saved.done,1);assert.deepEqual(saved.pending,['b']);
});
test('restoring running state never starts traffic or loses counters',()=>{
 let calls=0;
 const c=create({initial:{kind:'checkpoints',pending:['b'],total:2,done:1,updates:1,status:'running'},check:()=>calls++});
 assert.equal(calls,0);assert.equal(c.snapshot().status,'paused');assert.equal(c.snapshot().updates,1);
});
test('malformed persisted state is ignored',()=>{
 const c=create({initial:{kind:'loras',pending:['b'],total:2,done:5,updates:1},check:()=>{}});
 assert.equal(c.snapshot().status,'idle');
});
