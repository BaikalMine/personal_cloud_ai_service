"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { createRepeatController } = require("../../static/generation-repeat-state.js");
const sourceID = "generation-source-0001", requestID = "generation-repeat-0001";
const deferred = () => { let resolve, reject; const promise = new Promise((a,b) => { resolve=a; reject=b; }); return {promise,resolve,reject}; };

test("immediate repeat checks first and submits once without another decision", async () => {
  const f=fixture(); await f.controller.open(sourceID, {immediate:true});
  assert.deepEqual(f.calls.map(call=>call[0]), ["check","submit"]);
  assert.equal(f.controller.snapshot().phase,"accepted");
  await f.controller.submit(); assert.equal(f.calls.length,2);
});
test("immediate repeat does not silently approve changed or blocked parameters", async () => {
  for(const state of ["changed","blocked"]) {
    const f=fixture(); f.ready.state=state;
    await f.controller.open(sourceID,{immediate:true});
    assert.equal(f.calls.length,1); assert.equal(f.controller.snapshot().phase,state);
    f.ready.state="ready"; await f.controller.check();
    assert.equal(f.calls.filter(call=>call[0]==="submit").length,0);
  }
});
test("missing optional hashes do not require another click when parameters match", async () => {
  const f=fixture();f.ready.state="unverified";f.ready.differences=[];
  await f.controller.open(sourceID,{immediate:true});
  assert.deepEqual(f.calls.map(call=>call[0]),["check","submit"]);
  const changed=fixture();changed.ready.state="unverified";changed.ready.differences=[{label:"Steps",before:"6",after:"8"}];
  await changed.controller.open(sourceID,{immediate:true});assert.equal(changed.calls.length,1);
});
test("closing immediate inspection cancels launch intent", async () => {
  const gate=deferred(), f=fixture({check:()=>gate.promise});
  const opening=f.controller.open(sourceID,{immediate:true}); f.controller.setVisible(false);
  gate.resolve(f.ready); await opening;
  assert.equal(f.calls.filter(call=>call[0]==="submit").length,0);
});
test("pending immediate inspection can finish but recovery cannot launch a second job", async () => {
  let sends=0;
  const f=fixture({submit:async()=>{sends++;throw Error("lost");}});
  f.ready.state="pending"; await f.controller.open(sourceID,{immediate:true});
  f.ready.state="ready"; await f.controller.check(); assert.equal(sends,1);
  await f.controller.open(sourceID,{immediate:true});
  assert.equal(sends,1); assert.equal(f.controller.snapshot().phase,"accepted");
});
function fixture(overrides = {}) {
  let saved = null, ids = 0;
  const calls = [];
  const ready = { version:1, source_id:sourceID, state:"ready", seed:"9223372036854775806", confirmation:"opaque", expires_at:10000 };
  const receipt = { accepted:true, source_id:sourceID, request_id:requestID, job_id:"generation-child-0001" };
  const storage = { load:()=>saved, save:value=>{saved=structuredClone(value);}, clear:()=>{saved=null;} };
  const deps = { now:()=>1000, schedule:()=>1, unschedule:()=>{}, storage,
    newID:()=>{ids++;return requestID;}, check:async value=>{calls.push(["check",value]);return ready;},
    submit:async value=>{calls.push(["submit",value]);return receipt;}, recover:async value=>{calls.push(["recover",value]);return receipt;},
    discard:async value=>{calls.push(["discard",value]);return {...receipt,accepted:false,cancelled:true,job_id:undefined};}, ...overrides };
  return {controller:createRepeatController(deps),deps,ready,receipt,calls,storage,get saved(){return saved;},get ids(){return ids;}};
}
test("check never submits, exact seed survives, explicit confirmation is durable",async()=>{
  const f=fixture(); await f.controller.open(sourceID);
  assert.equal(f.controller.snapshot().view.seed,"9223372036854775806"); assert.equal(f.calls.length,1);
  await f.controller.submit(); assert.equal(f.controller.snapshot().phase,"accepted"); assert.equal(f.saved,null);
  assert.equal(f.calls.filter(x=>x[0]==="submit").length,1);
  await f.controller.submit(); assert.equal(f.calls.length,2);
});
test("storage failure and failed readback both prevent POST",async()=>{
  for (const storage of [{load:()=>null,save:()=>{throw Error("denied");},clear(){}},{load:()=>null,save(){},clear(){}}]) {
    const f=fixture({storage}); await f.controller.open(sourceID); await f.controller.submit();
    assert.equal(f.controller.snapshot().phase,"storage_error"); assert.equal(f.calls.length,1);
  }
});
test("unreadable stored identity blocks replacement",async()=>{
  const f=fixture({storage:{load:()=>({version:5}),save(){},clear(){}}});
  await f.controller.open(sourceID); assert.equal(f.controller.snapshot().phase,"storage_error"); assert.equal(f.ids,0);
});
test("lost response survives close/reload and recovers by GET only",async()=>{
  const f=fixture({submit:async()=>{throw Error("offline");}});
  await f.controller.open(sourceID); await f.controller.submit(); f.controller.setVisible(false);
  assert.equal(f.controller.snapshot().phase,"uncertain"); assert.equal(f.saved.submitted,true);
  const next=createRepeatController(f.deps); await next.open("different-source-0001");
  assert.equal(next.snapshot().phase,"accepted"); assert.equal(f.ids,1);
  assert.equal(f.calls.at(-1)[0],"recover"); assert.equal(f.calls.at(-1)[1].sourceID,sourceID);
});
test("404 is not proof of absence; same identity remains until negative fence",async()=>{
  const f=fixture({submit:async()=>{throw Error("lost");},recover:async()=>{throw Object.assign(Error("absent"),{status:404});}});
  await f.controller.open(sourceID); await f.controller.submit(); await f.controller.recover();
  assert.equal(f.controller.snapshot().phase,"uncertain"); assert.ok(f.saved);
  await f.controller.open("different-source-0001"); assert.equal(f.ids,1);
  await f.controller.discard(); assert.equal(f.controller.snapshot().phase,"cancelled"); assert.equal(f.saved,null);
});
test("discard after acceptance reports the existing task",async()=>{
  const f=fixture({submit:async()=>{throw Error("lost");}}); f.deps.discard=async()=>f.receipt;
  await f.controller.open(sourceID); await f.controller.submit(); await f.controller.discard();
  assert.equal(f.controller.snapshot().phase,"accepted"); assert.equal(f.controller.snapshot().receipt.job_id,f.receipt.job_id);
});
test("hiding a pending submit does not abort it or create a new identity",async()=>{
  const gate=deferred(); const f=fixture({submit:()=>gate.promise}); await f.controller.open(sourceID);
  const sending=f.controller.submit(); f.controller.setVisible(false); await f.controller.open("different-source-0001");
  assert.equal(f.ids,1); assert.equal(f.controller.snapshot().phase,"submitting");
  gate.resolve(f.receipt); await sending; assert.equal(f.controller.snapshot().phase,"accepted");
});
test("obsolete checks are aborted and cannot replace a new source",async()=>{
  const gate=deferred(); let signal;
  const f=fixture({check:async(value,s)=>{if(value.sourceID===sourceID){signal=s;return gate.promise;}return {...f.ready,source_id:value.sourceID};}});
  const first=f.controller.open(sourceID); await f.controller.open("different-source-0001");
  assert.equal(signal.aborted,true); gate.resolve(f.ready); await first;
  assert.equal(f.controller.snapshot().sourceID,"different-source-0001");
  assert.equal(f.controller.snapshot().view.source_id,"different-source-0001");
});
test("expired confirmation, pending and blocked cannot submit",async()=>{
  for(const state of ["pending","blocked"]) {
    const f=fixture(); f.ready.state=state; await f.controller.open(sourceID); await f.controller.submit(); assert.equal(f.calls.length,1);
  }
  let time=1000; const f=fixture({now:()=>time}); await f.controller.open(sourceID); time=20000;
  await f.controller.submit(); assert.equal(f.calls.length,1); f.controller.setVisible(true); assert.equal(f.controller.snapshot().phase,"expired");
});
test("mismatched acknowledgement stays recoverable",async()=>{
  const f=fixture({submit:async()=>({...f.receipt,request_id:"different-request-0001"})});
  await f.controller.open(sourceID); await f.controller.submit(); assert.equal(f.controller.snapshot().phase,"uncertain"); assert.ok(f.saved);
});
test("explicit recheck after uncertainty preserves identity and never automatically posts",async()=>{
  const f=fixture({submit:async()=>{throw Error("lost");}}); await f.controller.open(sourceID); await f.controller.submit();
  await f.controller.check(); assert.equal(f.controller.snapshot().phase,"ready"); assert.equal(f.controller.snapshot().requestID,requestID); assert.ok(f.saved);
  assert.equal(f.ids,1); assert.equal(f.calls.at(-1)[0],"check");
});
test("hidden pending checks do not poll; visible pending checks can poll",async()=>{
  let callback; const f=fixture({schedule:fn=>{callback=fn;return 1;}}); f.ready.state="pending";
  await f.controller.open(sourceID); assert.equal(typeof callback,"function"); f.controller.setVisible(false);
  assert.equal(f.calls.length,1);
});
