import asyncio
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import threading
import types
import unittest

from aiohttp import web
from aiohttp.test_utils import TestClient, TestServer


spec = importlib.util.spec_from_file_location("gpu_guard", Path(__file__).with_name("guard.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class FakeClient:
    token = "k" * 64

    def __init__(self):
        self.calls = []
        self.states = {}
        self.fail = None
        self.reject = None

    async def call(self, action, permit, code=None):
        self.calls.append((action, dict(permit), code))
        if action == self.reject:
            raise module.Rejected("fixture reject")
        if action == "reserve":
            if any(value != "closed" for value in self.states.values()):
                raise module.Rejected("busy")
            self.states[permit["id"]] = "reserved"
        if action == "start":
            if self.states.get(permit["id"]) != "reserved":
                raise module.Rejected("already started")
            self.states[permit["id"]] = "running"
        if action == "finish":
            self.states[permit["id"]] = "closed"
        if action == self.fail:
            raise OSError("lost response")
        return {"protocol": 1, "id": permit["id"], "started": action == "start", "state": self.states[permit["id"]]}


def fixture(loop):
    class Queue:
        def __init__(self):
            self.mutex = threading.RLock()
            self.queue, self.currently_running, self.history = [], {}, {}
            self.counter = 0

        def put(self, item):
            with self.mutex:
                self.queue.append(item)

        def get(self, timeout=None):
            with self.mutex:
                if not self.queue:
                    return None
                item = self.queue.pop(0)
                identity = self.counter
                self.counter += 1
                self.currently_running[identity] = item
                return item, identity

        def task_done(self, item_id, outputs, status=None):
            item = self.currently_running.pop(item_id)
            self.history[item[1]] = copy.deepcopy({"prompt": item, "outputs": outputs})

        def wipe_queue(self):
            self.queue = []

        def delete_queue_item(self, predicate):
            for item in self.queue:
                if predicate(item):
                    self.queue.remove(item)
                    return True
            return False

    class Executor:
        def __init__(self):
            self.history_result = None
            self.reset_count = 0

        def execute(self, prompt, prompt_id, extra_data, outputs):
            self.history_result = {"image": "fixture.png"}

        def reset(self):
            self.reset_count += 1
            self.history_result = None

    server = types.SimpleNamespace(app=web.Application(), routes=web.RouteTableDef(), loop=loop, prompt_queue=Queue())
    return server, Executor


class GuardTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.server, self.Executor = fixture(asyncio.get_running_loop())
        self.cleanups = []

        def quiesce(executor):
            self.cleanups.append(executor)
            if self.cleanup_error:
                raise RuntimeError("live tensors")
            if executor:
                executor.reset()

        self.cleanup_error = False
        self.guard = module.Guard(self.server, self.Executor, quiesce)
        self.guard.configure("http://127.0.0.1:8091/internal/gpu/comfy/permit", "k" * 64,
                             Path(self.directory.name) / "receipts.sqlite",
                             storage_enabled=getattr(self, "storage_enabled", False))
        self.backend = FakeClient()
        self.guard.client = self.backend
        self.seen_headers = None
        self.validations = 0

        async def prompt(request):
            self.validations += 1
            self.seen_headers = dict(request.headers)
            data = await request.json()
            if data.get("invalid"):
                return web.json_response({"error": "invalid"}, status=400)
            if data.get("exception"):
                raise ValueError("validation failed")
            if data.get("detached"):
                task = asyncio.create_task(asyncio.to_thread(self.server.prompt_queue.put, (0, "detached", {}, {}, [])))
                await task
            self.server.prompt_queue.put((0, "prompt-fixture", data, {}, []))
            if data.get("twice"):
                self.server.prompt_queue.put((1, "second", data, {}, []))
            return web.json_response({"prompt_id": "prompt-fixture"})

        self.server.routes.post("/prompt")(prompt)
        self.server.routes.post("/api/prompt")(prompt)
        self.server.app.add_routes(self.server.routes)
        self.http = TestClient(TestServer(self.server.app))
        await self.http.start_server()

    async def asyncTearDown(self):
        await self.http.close()
        self.directory.cleanup()

    async def settled(self):
        for _ in range(20):
            if self.guard.tasks:
                await asyncio.gather(*list(self.guard.tasks))
            await asyncio.sleep(0)
            if not self.guard.tasks:
                break

    def worker(self, next_get=True):
        q = self.server.prompt_queue
        item, identity = q.get()
        executor = self.Executor()
        executor.execute(item[2], item[1], item[3], item[4])
        q.task_done(identity, executor.history_result)
        self.assertEqual(executor.reset_count, 0)
        self.assertEqual(executor.history_result, {"image": "fixture.png"})
        self.assertEqual(self.guard.receipts.all(), [])
        if next_get:
            self.assertIsNone(q.get())
        return executor

    async def test_worker_boundary_and_private_headers(self):
        headers = {module.KEY: "k" * 64, module.WORK: "owned-work", module.LEASE: "owned-lease"}
        response = await self.http.post("/api/prompt", json={"prompt": {}}, headers=headers)
        self.assertEqual(response.status, 200)
        self.assertFalse(any(k.lower().startswith(module.PREFIX) for k in self.seen_headers))
        self.assertEqual([call[0] for call in self.backend.calls], ["reserve", "start"])
        self.assertEqual(self.backend.calls[0][1]["work_id"], "owned-work")
        blocked = await self.http.post("/prompt", json={})
        self.assertEqual(blocked.status, 409)
        self.assertEqual(self.validations, 1)
        executor = await asyncio.to_thread(self.worker)
        await self.settled()
        self.assertEqual(executor.reset_count, 1)
        self.assertIsNone(self.guard.active)
        history = json.dumps(self.server.prompt_queue.history)
        self.assertNotIn("owned-lease", history)
        self.assertNotIn("k" * 64, history)
        self.assertEqual(self.backend.calls[-1][2], "quiesced")

    async def test_task_done_alone_is_not_completion(self):
        await self.http.post("/prompt", json={})
        await asyncio.to_thread(self.worker, False)
        await self.settled()
        self.assertIsNotNone(self.guard.active)
        self.assertEqual(self.cleanups, [])
        self.assertFalse(any(call[0] == "finish" for call in self.backend.calls))

    async def test_validation_failure_cleans_before_completion(self):
        response = await self.http.post("/prompt", json={"invalid": True})
        self.assertEqual(response.status, 400)
        await self.settled()
        self.assertEqual(self.cleanups, [None])
        self.assertIsNone(self.guard.active)
        self.assertEqual(self.backend.calls[-1][2], "quiesced")

    async def test_rejection_never_enters_handler(self):
        self.backend.reject = "reserve"
        response = await self.http.post("/prompt", json={})
        self.assertEqual(response.status, 409)
        self.assertEqual(self.validations, 0)
        self.assertIsNone(self.guard.active)
        self.assertEqual(self.guard.receipts.all(), [])

    async def test_lost_start_response_has_not_started_receipt(self):
        self.backend.fail = "start"
        response = await self.http.post("/prompt", json={})
        self.assertEqual(response.status, 503)
        await self.settled()
        self.assertEqual(self.validations, 0)
        self.assertIsNone(self.guard.active)
        self.assertEqual(self.backend.calls[-1][2], "not_started")

    async def test_lost_completion_replays_only_proved_receipt(self):
        self.backend.fail = "finish"
        await self.http.post("/prompt", json={})
        await asyncio.to_thread(self.worker)
        await self.settled()
        self.assertEqual(len(self.guard.receipts.all()), 1)
        previous = self.guard.receipts.all()
        reopened = module.Receipts(Path(self.directory.name) / "receipts.sqlite")
        self.assertEqual(reopened.all(), previous)
        self.assertIsNotNone(self.guard.active)
        self.backend.fail = None
        await self.guard.deliver()
        self.assertEqual(reopened.all(), [])
        self.assertIsNone(self.guard.active)

    async def test_unconfirmed_cleanup_keeps_permit(self):
        self.cleanup_error = True
        await self.http.post("/prompt", json={})
        await asyncio.to_thread(self.worker)
        await self.settled()
        self.assertFalse(self.guard.healthy())
        self.assertIsNotNone(self.guard.active)
        self.assertEqual(self.guard.receipts.all(), [])
        self.assertFalse(any(call[0] == "finish" for call in self.backend.calls))

    async def test_pending_wipe_and_delete(self):
        for method in ("wipe_queue", "delete_queue_item"):
            await self.http.post("/prompt", json={})
            if method == "wipe_queue":
                self.server.prompt_queue.wipe_queue()
            else:
                self.server.prompt_queue.delete_queue_item(lambda item: True)
            await self.settled()
            self.assertIsNone(self.guard.active)
            self.assertEqual(self.backend.calls[-1][2], "quiesced")

    async def test_running_wipe_does_not_release(self):
        await self.http.post("/prompt", json={})
        self.server.prompt_queue.get()
        self.server.prompt_queue.wipe_queue()
        await self.settled()
        self.assertIsNotNone(self.guard.active)
        self.assertEqual(self.guard.receipts.all(), [])

    async def test_direct_queue_and_executor_bypasses_fail(self):
        with self.assertRaises(RuntimeError):
            self.server.prompt_queue.put((0, "rogue", {}, {}, []))
        with self.assertRaises(RuntimeError):
            self.Executor().execute({}, "rogue", {}, [])
        self.assertEqual(self.server.prompt_queue.queue, [])

    async def test_double_put_cannot_reuse_one_permit(self):
        response = await self.http.post("/prompt", json={"twice": True})
        self.assertEqual(response.status, 500)
        self.assertEqual(len(self.server.prompt_queue.queue), 1)
        self.assertIsNotNone(self.guard.active)
        self.assertFalse(self.guard.healthy())
        self.assertEqual(self.guard.receipts.all(), [])

    async def test_hooks_and_status_authentication(self):
        response = await self.http.get("/ai-gateway/gpu/status")
        self.assertEqual(response.status, 404)
        response = await self.http.get("/ai-gateway/gpu/status", headers={module.KEY: "k" * 64})
        self.assertTrue((await response.json())["enforced"])
        self.server.prompt_queue.put = lambda item: None
        response = await self.http.get("/ai-gateway/gpu/status", headers={module.KEY: "k" * 64})
        self.assertFalse((await response.json())["enforced"])
        response = await self.http.post("/prompt", json={})
        self.assertEqual(response.status, 503)

    async def test_forged_or_partial_managed_headers(self):
        for headers in ({module.KEY: "k" * 64}, {module.KEY: "wrong", module.WORK: "w", module.LEASE: "l"}):
            response = await self.http.post("/prompt", json={}, headers=headers)
            self.assertEqual(response.status, 403)
        self.assertEqual(self.validations, 0)
        self.assertEqual(self.backend.calls, [])

    async def test_duplicate_cleanup_cannot_unload_the_next_job(self):
        await self.http.post("/prompt", json={})
        ticket = self.guard.active
        self.server.prompt_queue.wipe_queue()
        await self.settled()
        self.assertIsNone(self.guard.active)
        await self.http.post("/prompt", json={})
        count = len(self.cleanups)
        await self.guard.complete_without_worker(ticket, True)
        self.assertEqual(len(self.cleanups), count)
        self.assertIsNotNone(self.guard.active)

    async def test_unknown_restart_has_no_replayable_receipt(self):
        await self.http.post("/prompt", json={})
        reopened = module.Receipts(Path(self.directory.name) / "receipts.sqlite")
        self.assertEqual(reopened.all(), [])
        self.assertEqual(list(self.backend.states.values()), ["running"])

    async def test_detached_validation_cannot_submit(self):
        response = await self.http.post("/prompt", json={"detached": True})
        self.assertEqual(response.status, 500)
        await self.settled()
        self.assertEqual(self.server.prompt_queue.queue, [])
        self.assertIsNotNone(self.guard.active)
        self.assertFalse(self.guard.healthy())
        self.assertEqual(self.guard.receipts.all(), [])

    async def test_cancelled_validator_is_not_quiescence(self):
        entered = asyncio.Event()

        async def handler(request):
            entered.set()
            await asyncio.Event().wait()

        async def parse():
            return {}

        request = types.SimpleNamespace(headers={}, clone=lambda **kwargs: types.SimpleNamespace(json=parse))
        task = asyncio.create_task(self.guard.prompt(request, handler))
        await entered.wait()
        task.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await task
        await self.settled()
        self.assertIsNotNone(self.guard.active)
        self.assertEqual(self.cleanups, [])
        self.assertEqual(self.guard.receipts.all(), [])

    async def test_malformed_json_does_not_reserve_gpu(self):
        for body in ("{broken", "[]", "null"):
            response = await self.http.post("/prompt", data=body, headers={"Content-Type": "application/json"})
            self.assertEqual(response.status, 400)
        self.assertEqual(self.backend.calls, [])
        self.assertIsNone(self.guard.active)
        self.assertTrue(self.guard.healthy())

    async def test_concurrent_request_waits_before_validation(self):
        reserve_entered, reserve_release = asyncio.Event(), asyncio.Event()
        original = self.backend.call

        async def delayed(action, permit, code=None):
            if action == "reserve":
                reserve_entered.set()
                await reserve_release.wait()
            return await original(action, permit, code)

        self.backend.call = delayed
        first = asyncio.create_task(self.http.post("/prompt", json={}))
        await reserve_entered.wait()
        second = await self.http.post("/prompt", json={})
        self.assertEqual(second.status, 409)
        self.assertEqual(self.validations, 0)
        reserve_release.set()
        self.assertEqual((await first).status, 200)
        self.assertEqual(self.validations, 1)


class PermitClientTests(unittest.IsolatedAsyncioTestCase):
    async def test_bounded_strict_identity_and_no_redirects(self):
        permit = {"id": "a" * 32, "instance_id": "b" * 32, "token": "c" * 64}
        valid = {"protocol": 1, "id": permit["id"], "state": "reserved", "started": True}
        for status, payload, action, expected in [
            (200, json.dumps(valid), "reserve", None),
            (200, json.dumps(valid), "start", None),
            (200, json.dumps(dict(valid, id="d" * 32)), "start", RuntimeError),
            (200, json.dumps(dict(valid, started=False)), "start", RuntimeError),
            (200, json.dumps(dict(valid, protocol=2)), "reserve", RuntimeError),
            (200, json.dumps(valid) + " {}", "reserve", ValueError),
            (200, " " * 5000, "reserve", RuntimeError),
            (409, '{"protocol":1,"error":"executor_permit_unavailable"}', "reserve", module.Rejected),
            (503, "unavailable", "reserve", ValueError),
            (302, json.dumps(valid), "reserve", RuntimeError),
        ]:
            with self.subTest(status=status, action=action, payload=payload[:80]):
                calls = []

                async def callback(request):
                    calls.append(await request.json())
                    self.assertEqual(request.headers.get("Authorization"), "Bearer " + "k" * 64)
                    return web.Response(status=status, text=payload, headers={"Location": "/redirect"})

                app = web.Application()
                app.router.add_post("/internal/gpu/comfy/permit", callback)
                async with TestServer(app) as server:
                    client = module.PermitClient(str(server.make_url("/internal/gpu/comfy/permit")), "k" * 64)
                    if expected is None:
                        await client.call(action, permit)
                    else:
                        with self.assertRaises(expected):
                            await client.call(action, permit)
                self.assertEqual(len(calls), 1)


class QuiescenceTests(unittest.TestCase):
    def test_all_devices_are_synchronized_and_caches_released(self):
        events = []
        cuda = types.SimpleNamespace(is_available=lambda: True, device_count=lambda: 2,
            synchronize=lambda d: events.append(("sync", d)), memory_allocated=lambda d: 0, memory_reserved=lambda d: 0)
        models = types.SimpleNamespace(unload_all_models=lambda: events.append("unload"), soft_empty_cache=lambda: events.append("empty"))
        executor = types.SimpleNamespace(reset=lambda: events.append("reset"))
        module.cuda_quiescer(types.SimpleNamespace(cuda=cuda), models)(executor)
        self.assertEqual(events, [("sync", 0), ("sync", 1), "reset", "unload", "empty", ("sync", 0), ("sync", 1)])
        for field in ("memory_allocated", "memory_reserved"):
            setattr(cuda, field, lambda d: 1)
            with self.assertRaises(RuntimeError):
                module.cuda_quiescer(types.SimpleNamespace(cuda=cuda), models)(executor)
            setattr(cuda, field, lambda d: 0)
        cuda.is_available = lambda: False
        with self.assertRaises(RuntimeError):
            module.cuda_quiescer(types.SimpleNamespace(cuda=cuda), models)(executor)


class ReceiptTests(unittest.TestCase):
    def test_terminal_identity_cannot_change(self):
        with tempfile.TemporaryDirectory() as directory:
            receipts = module.Receipts(Path(directory) / "receipts.sqlite")
            permit = {"id": "a" * 32, "instance_id": "b" * 32, "token": "c" * 64}
            receipts.save(permit, "quiesced")
            receipts.save(permit, "quiesced")
            with self.assertRaises(RuntimeError):
                receipts.save(permit, "not_started")
            with self.assertRaises(RuntimeError):
                receipts.save(dict(permit, token="d" * 64), "quiesced")


if __name__ == "__main__":
    unittest.main()
