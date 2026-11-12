const test = require("node:test");
const assert = require("node:assert/strict");
const media = require("../../static/generation-media.js");

const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const tick = () => Promise.resolve();
const fixture = () => {
  let source = { name: "first.png" };
  const requests = [];
  const commits = [];
  let settled = 0;
  const controller = media.createUploadController({ settled: () => { settled++; } });
  const options = {
    field: "input_image", readSource: () => source,
    transfer: (captured) => { const request = deferred(); requests.push({ ...request, source: captured }); return request.promise; },
    commit: (value) => { commits.push(value); return value; },
  };
  return { controller, options, requests, commits, change: (next) => { source = next; }, settled: () => settled };
};

test("draft and submit share one upload and one commit", async () => {
  const f = fixture();
  const first = f.controller.upload(f.options);
  assert.equal(f.controller.upload(f.options), first);
  assert.equal(f.controller.pendingCount, 1);
  await tick();
  assert.equal(f.requests.length, 1);
  f.requests[0].resolve("session/file.png");
  assert.equal(await first, "session/file.png");
  assert.deepEqual(f.commits, ["session/file.png"]);
  assert.equal(f.controller.pendingCount, 0);
  assert.equal(f.settled(), 1);
});

test("replaced source cannot commit or remove the new pending request", async () => {
  const f = fixture();
  const first = f.controller.upload(f.options);
  const rejected = assert.rejects(first, /source changed/);
  await tick();
  f.change({ name: "second.png" });
  const second = f.controller.upload(f.options);
  await tick();
  f.requests[0].resolve("old");
  await rejected;
  assert.equal(f.controller.pendingCount, 1);
  assert.equal(f.controller.upload(f.options), second);
  f.requests[1].resolve("new");
  await second;
  assert.deepEqual(f.commits, ["new"]);
});

test("all in-flight uploads remain counted when newest settles first", async () => {
  const f = fixture();
  const first = f.controller.upload(f.options);
  const rejected = assert.rejects(first, /source changed/);
  await tick();
  f.change({ name: "second.png" });
  const second = f.controller.upload(f.options);
  await tick();
  f.requests[1].resolve("new");
  await second;
  assert.equal(f.controller.pendingCount, 1);
  f.requests[0].resolve("old");
  await rejected;
  assert.equal(f.controller.pendingCount, 0);
  assert.deepEqual(f.commits, ["new"]);
});

test("switching back to an old source does not revive an obsolete request", async () => {
  const f = fixture();
  const original = f.options.readSource();
  const first = f.controller.upload(f.options);
  const rejected = assert.rejects(first, /source changed/);
  await tick();
  f.change({ name: "second.png" });
  const second = f.controller.upload(f.options);
  const rejectedSecond = assert.rejects(second, /source changed/);
  await tick();
  f.change(original);
  f.requests[0].resolve("old");
  f.requests[1].resolve("other");
  await rejected;
  await rejectedSecond;
  assert.deepEqual(f.commits, []);
});

test("source cleared before transport causes no request", async () => {
  const f = fixture();
  const first = f.controller.upload(f.options);
  f.change(null);
  await assert.rejects(first, /source changed/);
  assert.equal(f.requests.length, 0);
  assert.equal(f.controller.pendingCount, 0);
  await assert.rejects(f.controller.upload(f.options), /Файл не выбран/);
});

test("failure clears ownership and allows retry without a commit", async () => {
  const f = fixture();
  const first = f.controller.upload(f.options);
  const rejected = assert.rejects(first, /offline/);
  await tick();
  f.requests[0].reject(new Error("offline"));
  await rejected;
  const retry = f.controller.upload(f.options);
  await tick();
  f.requests[1].resolve("retry");
  await retry;
  assert.deepEqual(f.commits, ["retry"]);
  assert.equal(f.settled(), 2);
});

test("synchronous transfer error releases pending ownership", async () => {
  const f = fixture();
  await assert.rejects(f.controller.upload({ ...f.options, transfer: () => { throw new Error("transport"); } }), /transport/);
  assert.equal(f.controller.pendingCount, 0);
});

test("prepare is shared, serial and revalidates before ready", async () => {
  const controller = media.createUploadController();
  const first = deferred();
  const events = [];
  let entries = [
    { upload: async () => { events.push("first"); await first.promise; entries.shift(); } },
    { upload: async () => { events.push("second"); entries.shift(); } },
  ];
  const options = { selection: () => "mode", validate: () => events.push("validate"), pending: () => [...entries], ready: () => events.push("ready") };
  const preparing = controller.prepare(options);
  assert.equal(controller.prepare(options), preparing);
  await tick();
  assert.deepEqual(events, ["validate", "first"]);
  first.resolve();
  await preparing;
  assert.deepEqual(events, ["validate", "first", "second", "validate", "ready"]);
});

for (const change of ["selection", "pending", "validation"]) {
  test(`prepare refuses changed ${change} and can run again`, async () => {
    const controller = media.createUploadController();
    const gate = deferred();
    let selection = "initial", invalid = false, ready = 0;
    let entries = [{ upload: async () => { await gate.promise; entries = []; } }];
    const options = {
      selection: () => selection,
      validate: () => { if (invalid) throw new Error("invalid"); },
      pending: () => change === "pending" && invalid ? [{}] : [...entries],
      ready: () => { ready++; },
    };
    const preparing = controller.prepare(options);
    const rejected = assert.rejects(preparing, /changed|invalid/);
    await tick();
    if (change === "selection") selection = "new"; else invalid = true;
    gate.resolve();
    await rejected;
    assert.equal(ready, 0);
    invalid = false;
    await controller.prepare(options);
    assert.equal(ready, 1);
  });
}

test("prepare reports upload errors once and does not continue the list", async () => {
  const events = [];
  const controller = media.createUploadController();
  await assert.rejects(controller.prepare({
    selection: () => "", validate() {},
    pending: () => [{ upload: async () => { throw new Error("offline"); } }, { upload: () => assert.fail("second upload") }],
    progress: (_entry, state, error) => events.push([state, error?.message]),
    ready: () => assert.fail("ready"),
  }), /offline/);
  assert.deepEqual(events, [["loading", undefined], ["error", "offline"]]);
});

for (const kind of ["image", "audio", "video"]) {
  test(`device ${kind} preserves multipart contract`, async () => {
    const file = { name: `reference.${kind}`, size: 12 };
    const fields = [];
    const result = await media.uploadSource(file, {
      kind, formDataFactory: () => ({ append: (...entry) => fields.push(entry) }),
      fetcher: async (url, request) => {
        assert.equal(url, `/generate/upload/${kind}`);
        assert.equal(request.method, "POST");
        assert.equal(request.credentials, "same-origin");
        assert.deepEqual(request.headers, { "X-Upload-File-Size": "12" });
        return { ok: true, json: async () => ({ name: "accepted", subfolder: "session" }) };
      },
    });
    assert.deepEqual(fields, [["image", file, file.name], ["type", "input"], ["overwrite", "true"]]);
    assert.equal(result.value, "session/accepted");
    assert.equal(result.source.file, file);
  });
}

test("gallery reuse retains CSRF and media ID without device upload", async () => {
  const result = await media.uploadImageSource({ id: "media-id", filename: "photo.png" }, {
    csrf: "csrf-value",
    fetcher: async (url, request) => {
      assert.equal(url, "/generate/library/reuse-image");
      assert.equal(request.headers["Content-Type"], "application/x-www-form-urlencoded;charset=UTF-8");
      assert.deepEqual(Object.fromEntries(request.body), { csrf: "csrf-value", media_id: "media-id" });
      assert.equal(request.credentials, "same-origin");
      return { ok: true, json: async () => ({ name: "photo.png" }) };
    },
  });
  assert.equal(result.value, "photo.png");
});

test("invalid transport sources never issue requests", async () => {
  const options = { fetcher: () => assert.fail("network") };
  await assert.rejects(media.uploadSource(null, options), /required/);
  await assert.rejects(media.uploadSource({ kind: "restored" }, options), /Restored/);
  await assert.rejects(media.uploadSource({ kind: "device" }, options), /required/);
  await assert.rejects(media.uploadSource({ name: "file" }, { ...options, kind: "script" }), /Unsupported/);
  await assert.rejects(media.uploadSource({ kind: "gallery", id: "id" }, { ...options, kind: "video" }), /only available/);
});

test("transport failures preserve server errors and handle malformed bodies", async () => {
  for (const body of [null, {}, { error: "quota" }]) {
    await assert.rejects(media.uploadSource({ name: "file" }, {
      formDataFactory: () => ({ append() {} }), errorMessage: "upload failed",
      fetcher: async () => ({ ok: false, json: async () => body }),
    }), body?.error ? /quota/ : /upload failed/);
  }
  await assert.rejects(media.uploadSource({ name: "file" }, {
    formDataFactory: () => ({ append() {} }),
    fetcher: async () => ({ ok: true, json: async () => { throw new Error("invalid JSON"); } }),
  }), /upload failed/);
});

test("unavailable ComfyUI is not reported as a rejected photo", async () => {
  await assert.rejects(media.uploadImageSource({ name: "photo.png" }, {
    formDataFactory: () => ({ append() {} }), errorMessage: "ComfyUI не принял фото",
    fetcher: async () => ({ ok: false, status: 502, json: async () => { throw new Error("HTML gateway error"); } }),
  }), /ComfyUI временно недоступен/);
});
