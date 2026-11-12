const test = require("node:test");
const assert = require("node:assert/strict");
const { createController } = require("../../static/generation-assistant-jobs.js");
const id = "assistant-test-request-0001";
const tick = () => new Promise(resolve => setImmediate(resolve));
function fixture(transport) {
  let time = 100000, serial = 0, saved;
  const timers = new Map(), states = [], jobs = [], calls = [];
  const controller = createController({
    transport: async (...args) => { calls.push(args[0]); return transport(...args); },
    persist: value => { saved = value; }, onState: value => states.push(value), onJob: value => jobs.push(value),
    now: () => time, schedule: (fn, delay) => { const key = ++serial; timers.set(key, { fn, at: time + delay }); return key; }, unschedule: key => timers.delete(key),
  });
  return { controller, states, jobs, calls, saved: () => saved,
    advance: async amount => { const target = time + amount; for (;;) { const next = [...timers].sort((a,b) => a[1].at-b[1].at)[0]; if (!next || next[1].at > target) break; time=next[1].at; timers.delete(next[0]); next[1].fn(); await tick(); } time=target; await tick(); },
  };
}
const response = state => ({ ok:true,status:200,payload:{id,state,status:state,expires_at:new Date(86400000).toISOString(),result:state === "completed" ? {prompt:"A vase"}:undefined} });

test("lost POST is recovered by GET, never by posting another model call", async () => {
  const f = fixture(async op => { if (op === "submit") throw new TypeError("Failed to fetch"); return response("completed"); });
  await f.controller.start(id, new URLSearchParams());
  assert.equal(f.saved().id, id);
  assert.equal(f.controller.snapshot().pending, true);
  assert.ok(f.states.some(value => /повторно модель не запускаем/.test(value.message)));
  assert.ok(f.states.every(value => !/Failed to fetch/.test(value.message)));
  await f.advance(6000);
  assert.deepEqual(f.calls, ["submit","get"]);
  assert.equal(f.jobs[0].state,"completed");
  assert.equal(f.controller.snapshot().pending, false);
  assert.ok(f.states.some(value => value.retrySeconds === 5));
});
test("missing receipt does not allow a duplicate; explicit cancellation fences the original ID", async () => {
  const f=fixture(async op => op==="cancel" ? response("cancelled") : {ok:false,status:404,payload:{}});
  f.controller.resume({id}); await tick();
  assert.equal(f.controller.snapshot().pending,true);
  await assert.rejects(f.controller.start("different-request-0001",new URLSearchParams()),/не завершён/);
  f.controller.cancel(); await tick();
  assert.equal(f.jobs.at(-1).state,"cancelled");
  assert.deepEqual(f.calls,["get","cancel"]);
});
test("pagehide stops polling without cancelling the server job; pageshow resumes the same ID", async () => {
  const f=fixture(async()=>response("running"));
  f.controller.resume({id}); await tick();
  f.controller.suspend(); await f.advance(20000);
  assert.deepEqual(f.calls,["get"]);
  f.controller.resume(); await tick();
  assert.deepEqual(f.calls,["get","get"]);
});
test("late POST response after cancellation cannot resurrect completed UI state", async () => {
  let release;
  const f=fixture(op => op==="submit" ? new Promise(resolve=>{release=resolve;}) : Promise.resolve(response("cancelled")));
  const posting=f.controller.start(id,new URLSearchParams()); await tick();
  f.controller.cancel(); await tick();
  release(response("completed")); await posting;
  assert.deepEqual(f.jobs.map(job=>job.state),["cancelled"]);
  assert.equal(f.controller.snapshot().pending,false);
});
test("storage failure prevents dispatch and rejected submissions do not restore as pending", async () => {
  let calls=0;
  const c=createController({transport:()=>{calls++;},persist:()=>{throw new Error("quota");}});
  await assert.rejects(c.start(id,new URLSearchParams()),/сохранить запрос/);
  assert.equal(calls,0);
  const f=fixture(async()=>({ok:false,status:422,payload:{error:"Invalid prompt"}}));
  await f.controller.start(id,new URLSearchParams());
  assert.equal(f.saved(),null);
  assert.equal(f.controller.snapshot().pending,false);
  assert.equal(f.jobs[0].status,"Invalid prompt");
});
test("cancellation survives reload and polls cancel, not submit", async () => {
  const f=fixture(async()=>response("cancelled"));
  f.controller.resume({id,cancelling:true}); await tick();
  assert.deepEqual(f.calls,["cancel"]);
  assert.equal(f.controller.snapshot().pending,false);
});
test("expired local pointers do not run or poll a new model request", async () => {
  const f=fixture(async()=>response("running"));
  f.controller.resume({id,expiresAt:50000}); await tick();
  assert.deepEqual(f.calls,[]);
  assert.match(f.jobs[0].status,/истёк/);
});

test("POST lost before acceptance resends exactly the same ID and immutable payload", async () => {
  const bodies = [];
  const f = fixture(async (op, body) => {
    if (op === "get") return { ok: false, status: 404, payload: {} };
    bodies.push(body.toString());
    if (bodies.length === 1) throw new TypeError("Failed to fetch");
    return response("completed");
  });
  const input = new URLSearchParams({ prompt: "A vase", input_image: "saved.png" });
  await f.controller.start(id, input);
  input.set("prompt", "Later edit must not change the submitted job");
  f.controller.suspend();
  f.controller.resume();
  await tick();
  assert.deepEqual(f.calls, ["submit", "get", "submit"]);
  assert.equal(bodies[0], bodies[1]);
  assert.equal(new URLSearchParams(bodies[1]).get("request_id"), id);
  assert.equal(f.controller.snapshot().pending, false);
  assert.equal(JSON.stringify(f.saved()).includes("A vase"), false);
});

test("accepted receipt drops retry payload and cancellation never resubmits", async () => {
  let accepted = false;
  const f = fixture(async op => {
    if (op === "submit") { accepted = true; return response("running"); }
    if (op === "cancel") return response("cancelled");
    return { ok: false, status: 404, payload: {} };
  });
  await f.controller.start(id, new URLSearchParams({ prompt: "A vase" }));
  await f.advance(2000);
  assert.equal(accepted, true);
  assert.deepEqual(f.calls, ["submit", "get"]);
  f.controller.cancel(); await tick();
  assert.deepEqual(f.calls, ["submit", "get", "cancel"]);
});

test("rejected recovery POST retains identity until original arrival is resolved", async () => {
  let posts = 0, reads = 0;
  const f = fixture(async op => {
    if (op === "submit") {
      if (++posts === 1) throw new TypeError("Failed to fetch");
      return { ok: false, status: 409, payload: { error: "temporarily unavailable" } };
    }
    return ++reads === 1 ? { ok: false, status: 404, payload: {} } : response("completed");
  });
  await f.controller.start(id, new URLSearchParams());
  await f.advance(6000);
  assert.equal(f.saved().id, id);
  assert.equal(f.controller.snapshot().pending, true);
  await f.advance(12000);
  assert.equal(f.controller.snapshot().pending, false);
  assert.equal(posts, 2);
});
