import asyncio
from pathlib import Path
import sqlite3
import tempfile
import threading
import unittest
from unittest.mock import patch

from aiohttp import web
from aiohttp.test_utils import TestClient, TestServer

import test_guard


module = test_guard.module


class EnabledGPURegressionTests(test_guard.GuardTests):
    storage_enabled = True


class StorageTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.path = Path(self.directory.name) / "receipts.sqlite"
        self.requests = 0
        self.entered = asyncio.Event()
        self.release = asyncio.Event()
        await self.start()

    async def start(self, enabled=True):
        self.server, self.Executor = test_guard.fixture(asyncio.get_running_loop())
        self.guard = module.Guard(self.server, self.Executor, lambda executor: None)
        self.guard.configure("http://127.0.0.1:8091/internal/gpu/comfy/permit", "k" * 64,
                             self.path, storage_enabled=enabled)
        self.guard.client = test_guard.FakeClient()

        async def upload(request):
            self.requests += 1
            if request.query.get("wait"):
                self.entered.set()
                await self.release.wait()
            if request.query.get("crash"):
                raise RuntimeError("synthetic interrupted writer")
            if request.query.get("reject"):
                raise web.HTTPBadRequest()
            return web.json_response({"ok": True})

        async def prompt(request):
            self.requests += 1
            self.server.prompt_queue.put((0, "fixture", {}, {}, []))
            return web.json_response({"prompt_id": "fixture"})

        for path in ("/upload/image", "/upload/mask", "/api/upload/image", "/api/upload/mask", "/custom/write"):
            self.server.routes.post(path)(upload)
        self.server.routes.put("/custom/write")(upload)
        self.server.routes.delete("/custom/write")(upload)
        self.server.routes.get("/view")(upload)
        self.server.routes.post("/prompt")(prompt)
        self.server.routes.post("/api/prompt")(prompt)
        self.server.app.add_routes(self.server.routes)
        self.http = TestClient(TestServer(self.server.app))
        await self.http.start_server()

    async def asyncTearDown(self):
        self.release.set()
        await self.http.close()
        self.directory.cleanup()

    def payload(self, action="hold", identity="operation-storage-0001", **changes):
        data = {"protocol": 1, "action": action, "id": identity, "request_hash": "a" * 64,
                "executor_id": "b" * 64, "journal_id": self.guard.storage.identity}
        if action == "close":
            data["executor_fenced"] = True
        return dict(data, **changes)

    async def operation(self, action="hold", identity="operation-storage-0001", expected=200, **changes):
        response = await self.http.post("/ai-gateway/storage/window", json=self.payload(action, identity, **changes),
                                        headers={module.KEY: "k" * 64})
        self.assertEqual(response.status, expected, await response.text())
        self.assertEqual(response.headers["Cache-Control"], "no-store")
        return await response.json()

    async def test_hold_blocks_prompts_and_all_http_writes_but_not_reads(self):
        held = await self.operation()
        self.assertEqual(held["state"], "held")
        for path in ("/prompt", "/api/prompt", "/upload/image", "/upload/mask",
                     "/api/upload/image", "/api/upload/mask", "/custom/write"):
            self.assertEqual((await self.http.post(path, json={})).status, 409)
        for method in (self.http.put, self.http.delete):
            self.assertEqual((await method("/custom/write")).status, 409)
        self.assertEqual(self.requests, 0)
        self.assertEqual(self.guard.client.calls, [])
        self.assertEqual((await self.http.get("/view")).status, 200)
        self.assertEqual(await self.operation(), held)
        self.assertEqual(await self.operation("read"), held)
        self.assertEqual((await self.operation("close"))["state"], "closed")
        self.assertEqual((await self.http.post("/upload/image")).status, 200)
        self.assertEqual((await self.http.post("/prompt", json={})).status, 200)

    async def test_accepted_upload_excludes_hold_until_handler_finishes(self):
        task = asyncio.create_task(self.http.post("/upload/image?wait=1"))
        await self.entered.wait()
        try:
            await self.operation(expected=409)
            self.assertIsNone(self.guard.storage.current())
        finally:
            self.release.set()
            self.assertEqual((await task).status, 200)
        await self.operation()

    async def test_prompt_admission_and_queued_or_running_execution_exclude_hold(self):
        self.assertEqual((await self.http.post("/prompt", json={})).status, 200)
        await self.operation(expected=409)
        await asyncio.to_thread(self.server.prompt_queue.get)
        await self.operation(expected=409)

    async def test_pending_gpu_receipt_and_unknown_queue_exclude_hold(self):
        self.guard.receipts.save({"id": "pending"}, "not_started")
        await self.operation(expected=409)
        self.guard.receipts.delete("pending")
        self.server.prompt_queue.queue.append((0, "foreign", {}, {}, []))
        await self.operation(expected=409)
        self.server.prompt_queue.queue.clear()
        await self.operation()

    async def test_hold_survives_restart_and_late_close_cannot_release_another(self):
        first = await self.operation()
        old_instance = self.guard.instance
        await self.http.close()
        await self.start()
        self.assertNotEqual(old_instance, self.guard.instance)
        self.assertEqual(first, await self.operation("read"))
        self.assertEqual((await self.http.post("/upload/mask")).status, 409)
        await self.operation("close")
        self.assertEqual((await self.operation())["state"], "closed")
        await self.operation(identity="operation-storage-0002")
        await self.operation("close")
        self.assertEqual(self.guard.storage.current()["id"], "operation-storage-0002")

    async def test_close_unknown_fences_late_hold_without_affecting_current(self):
        await self.operation()
        await self.operation("close", identity="operation-never-sent")
        self.assertEqual((await self.operation(identity="operation-never-sent"))["state"], "closed")
        self.assertEqual(self.guard.storage.current()["id"], "operation-storage-0001")

    async def test_conflicting_identity_or_other_hold_is_rejected(self):
        await self.operation()
        for action in ("read", "hold", "close"):
            for change in ({"request_hash": "c" * 64}, {"executor_id": "d" * 64}, {"journal_id": "e" * 32}):
                await self.operation(action, expected=409, **change)
        await self.operation(identity="operation-storage-0002", expected=409)
        self.assertIsNotNone(self.guard.storage.current())

    async def test_authentication_and_body_validation(self):
        for key in (None, "incorrect"):
            response = await self.http.post("/ai-gateway/storage/window", json={},
                                            headers={} if key is None else {module.KEY: key})
            self.assertEqual(response.status, 404)
        for changes in ({"protocol": True}, {"id": "../unsafe"}, {"executor_id": "short"},
                        {"journal_id": []}, {"action": "force"}, {"extra": 1}, {"id": "x" * 2100}):
            await self.operation(expected=400, **changes)
        await self.operation("close", expected=400, executor_fenced=False)
        self.assertIsNone(self.guard.storage.current())

    async def test_journal_write_failure_does_not_grant_hold(self):
        with patch.object(self.guard.storage, "apply", side_effect=sqlite3.OperationalError("synthetic disk failure")):
            await self.operation(expected=503)
        self.assertFalse(self.guard.healthy())
        self.assertIsNone(self.guard.storage.current())
        self.assertEqual((await self.http.post("/upload/image")).status, 503)
        self.assertEqual((await self.http.post("/prompt", json={})).status, 503)

    async def test_journal_identity_loss_blocks_file_admission(self):
        with self.guard.receipts.connect() as db:
            db.execute("UPDATE storage_identity SET id=?", ("f" * 32,))
        self.assertEqual((await self.http.post("/upload/image")).status, 409)
        self.assertFalse(self.guard.healthy())
        self.assertEqual(self.requests, 0)

    async def test_interrupted_writer_blocks_later_hold(self):
        self.assertEqual((await self.http.post("/upload/image?crash=1")).status, 500)
        self.assertEqual(self.guard.writers, 0)
        self.assertFalse(self.guard.healthy())
        await self.operation(expected=503)

    async def test_expected_http_rejection_does_not_poison_admission(self):
        self.assertEqual((await self.http.post("/upload/image?reject=1")).status, 400)
        self.assertEqual((await self.http.post("/unknown-route")).status, 404)
        self.assertTrue(self.guard.healthy())
        self.assertEqual(self.guard.writers, 0)
        await self.operation()

    async def test_held_window_cannot_be_disabled_by_configuration(self):
        await self.operation()
        server, executor = test_guard.fixture(asyncio.get_running_loop())
        guard = module.Guard(server, executor, lambda _: None)
        with self.assertRaisesRegex(ValueError, "cannot disable"):
            guard.configure("http://127.0.0.1:8091/internal/gpu/comfy/permit", "k" * 64, self.path)
        self.assertFalse(guard.healthy())
        self.assertTrue(guard.storage_enabled)

    async def test_new_journal_cannot_accept_an_old_journal_request(self):
        old_id = self.guard.storage.identity
        await self.http.close()
        self.path = Path(self.directory.name) / "new.sqlite"
        await self.start()
        await self.operation(journal_id=old_id, expected=409)

    async def test_opt_out_keeps_endpoint_unavailable(self):
        await self.http.close()
        await self.start(enabled=False)
        response = await self.http.post("/ai-gateway/storage/window", json={}, headers={module.KEY: "k" * 64})
        self.assertEqual(response.status, 404)
        self.assertEqual((await self.http.post("/upload/image")).status, 200)

    async def test_concurrent_different_holds_have_exactly_one_winner(self):
        async def attempt(index):
            response = await self.http.post("/ai-gateway/storage/window",
                                            json=self.payload(identity=f"concurrent-hold-{index:04d}"),
                                            headers={module.KEY: "k" * 64})
            return response.status
        results = await asyncio.gather(*(attempt(index) for index in range(16)))
        self.assertEqual(results.count(200), 1)
        self.assertEqual(results.count(409), 15)

    async def test_lost_hold_reply_can_be_read_without_new_hold(self):
        self.assertEqual((await self.operation("read"))["state"], "unknown")
        response = await self.http.post("/ai-gateway/storage/window", json=self.payload(),
                                        headers={module.KEY: "k" * 64})
        response.close()
        self.assertEqual((await self.operation("read"))["state"], "held")
        self.assertEqual((await self.operation("close"))["state"], "closed")
        self.assertEqual((await self.operation())["state"], "closed")

    async def test_canceled_writer_does_not_prove_quiescence(self):
        async def writer(request):
            self.entered.set()
            await self.release.wait()
        task = asyncio.create_task(self.guard.mutation(None, writer))
        await self.entered.wait()
        task.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await task
        self.assertEqual(self.guard.writers, 0)
        await self.operation(expected=503)

    async def test_canceled_threaded_upload_can_write_after_handler_exit(self):
        entered, release, finished = threading.Event(), threading.Event(), threading.Event()
        target = Path(self.directory.name) / "late-upload.png"

        def write_later():
            entered.set()
            try:
                if release.wait(5):
                    target.write_bytes(b"late bytes")
            finally:
                finished.set()

        async def writer(request):
            await asyncio.to_thread(write_later)

        task = asyncio.create_task(self.guard.mutation(None, writer))
        try:
            self.assertTrue(await asyncio.to_thread(entered.wait, 5))
            task.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await task
            self.assertEqual(self.guard.writers, 0)
            self.assertFalse(target.exists())
            await self.operation(expected=503)
            release.set()
            self.assertTrue(await asyncio.to_thread(finished.wait, 5))
            self.assertEqual(target.read_bytes(), b"late bytes")
            await self.operation(expected=503)
            self.assertIsNone(self.guard.storage.current())
        finally:
            release.set()
            await asyncio.to_thread(finished.wait, 5)
            if not task.done():
                task.cancel()
                await asyncio.gather(task, return_exceptions=True)

    async def test_hold_during_unfinished_prompt_admission_is_rejected(self):
        original = self.guard.client.call
        async def reserve(action, permit, code=None):
            if action == "reserve":
                self.entered.set()
                await self.release.wait()
            return await original(action, permit, code)
        self.guard.client.call = reserve
        task = asyncio.create_task(self.http.post("/prompt", json={}))
        await self.entered.wait()
        try:
            await self.operation(expected=409)
        finally:
            self.release.set()
            self.assertEqual((await task).status, 200)

    async def test_changed_hook_refuses_storage_hold(self):
        self.server.prompt_queue.put = lambda _: None
        await self.operation(expected=503)
        self.assertIsNone(self.guard.storage.current())

    async def test_status_reports_hold_without_claiming_gpu_hooks_are_disabled(self):
        await self.operation()
        response = await self.http.get("/ai-gateway/gpu/status", headers={module.KEY: "k" * 64})
        data = await response.json()
        self.assertTrue(data["enforced"])
        self.assertTrue(data["storage_held"])
        self.assertEqual(data["storage_protocol"], 1)
        self.assertEqual(data["storage_journal_id"], self.guard.storage.identity)

    async def test_backup_preserves_held_and_closed_windows_without_mutation(self):
        await self.operation("close", identity="closed-storage-operation")
        await self.operation(identity="held-storage-operation-1")
        before = self.guard.storage.current()
        response = await self.http.get("/ai-gateway/storage/backup", headers={module.KEY: "k" * 64})
        self.assertEqual(response.status, 200)
        self.assertEqual(response.headers["Cache-Control"], "no-store")
        data = await response.json()
        self.assertEqual(data["journal_id"], self.guard.storage.identity)
        self.assertEqual([row["state"] for row in data["windows"]], ["closed", "held"])
        self.assertEqual(data["receipts"], [])
        self.assertEqual(self.guard.storage.current(), before)
        self.assertEqual((await self.http.post("/upload/image")).status, 409)

    async def test_backup_auth_query_and_active_writer(self):
        self.assertEqual((await self.http.get("/ai-gateway/storage/backup")).status, 404)
        response = await self.http.get("/ai-gateway/storage/backup?x=1", headers={module.KEY: "k" * 64})
        self.assertEqual(response.status, 400)
        task = asyncio.create_task(self.http.post("/upload/image?wait=1"))
        await self.entered.wait()
        try:
            response = await self.http.get("/ai-gateway/storage/backup", headers={module.KEY: "k" * 64})
            self.assertEqual(response.status, 200)
            self.assertEqual((await response.json())["windows"], [])
            self.assertEqual(self.guard.writers, 1)
            self.assertIsNone(self.guard.storage.current())
        finally:
            self.release.set()
            self.assertEqual((await task).status, 200)


if __name__ == "__main__":
    unittest.main()
