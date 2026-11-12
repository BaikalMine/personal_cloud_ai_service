"""Opt-in ComfyUI admission adapter. No prompt data belongs in its journal."""

import asyncio
from contextlib import contextmanager
import contextvars
import hmac
import json
import logging
from pathlib import Path
import re
import secrets
import sqlite3
import threading
from urllib.parse import urlsplit

import aiohttp
from aiohttp import web

if __package__:
    from .storage_backup import snapshot as storage_snapshot
else:
    from storage_backup import snapshot as storage_snapshot


PREFIX = "x-ai-gateway-gpu-"
KEY = PREFIX + "key"
WORK = PREFIX + "work"
LEASE = PREFIX + "lease"
CURRENT = contextvars.ContextVar("ai_gateway_gpu_permit", default=None)
LOG = logging.getLogger("ai_gateway_gpu_guard")


class Rejected(Exception):
    """A complete, validated server rejection, not a transport ambiguity."""


class PermitClient:
    def __init__(self, endpoint, token):
        parts = urlsplit(endpoint)
        if (parts.scheme not in ("http", "https") or not parts.hostname or
                parts.username or parts.password or parts.query or parts.fragment or
                parts.path != "/internal/gpu/comfy/permit"):
            raise ValueError("invalid Gateway callback endpoint")
        if not isinstance(token, str) or not 32 <= len(token) <= 256 or not token.isascii() or any(c.isspace() for c in token):
            raise ValueError("invalid Gateway guard key")
        self.endpoint, self.token = endpoint, token

    async def call(self, action, permit, code=None):
        payload = dict(permit, action=action)
        if code is not None:
            payload["code"] = code
        timeout = aiohttp.ClientTimeout(total=12)
        async with aiohttp.ClientSession(timeout=timeout, trust_env=False) as session:
            async with session.post(self.endpoint, json=payload, allow_redirects=False,
                                    headers={"Authorization": "Bearer " + self.token}) as response:
                body = bytearray()
                async for chunk in response.content.iter_chunked(1024):
                    body.extend(chunk)
                    if len(body) > 4096:
                        raise RuntimeError("oversize permit response")
                data = json.loads(body)
                if not isinstance(data, dict) or data.get("protocol") != 1:
                    raise RuntimeError("invalid permit response")
                if response.status != 200:
                    if response.status in (400, 409, 503) and data.get("error") == "executor_permit_unavailable":
                        raise Rejected("permit rejected")
                    raise RuntimeError("unconfirmed permit response")
                if data.get("id") != permit["id"]:
                    raise RuntimeError("permit response identity mismatch")
                if action == "reserve" and data.get("state") != "reserved":
                    raise Rejected("permit is not reserved")
                if action == "start" and data.get("started") is not True:
                    raise RuntimeError("permit start not confirmed")
                return data


class Receipts:
    """Only locally proved terminal outcomes are replayable after a restart."""

    def __init__(self, filename):
        self.filename = str(filename)
        with self.connect() as db:
            db.execute("CREATE TABLE IF NOT EXISTS receipts (id TEXT PRIMARY KEY, payload TEXT NOT NULL, code TEXT NOT NULL CHECK(code IN ('not_started','quiesced')))")

    @contextmanager
    def connect(self):
        db = sqlite3.connect(self.filename, timeout=10)
        try:
            if db.execute("SELECT 1 FROM sqlite_master WHERE name='isolated_restore_blocked'").fetchone():
                raise RuntimeError("isolated restored journal cannot authorize live work")
            db.execute("PRAGMA synchronous=FULL")
            with db:
                yield db
        finally:
            db.close()

    def save(self, permit, code):
        payload = json.dumps(permit, sort_keys=True, separators=(",", ":"))
        with self.connect() as db:
            previous = db.execute("SELECT payload,code FROM receipts WHERE id=?", (permit["id"],)).fetchone()
            if previous and previous != (payload, code):
                raise RuntimeError("conflicting terminal receipt")
            db.execute("INSERT OR IGNORE INTO receipts(id,payload,code) VALUES(?,?,?)", (permit["id"], payload, code))

    def all(self):
        with self.connect() as db:
            return [(json.loads(payload), code) for payload, code in db.execute("SELECT payload,code FROM receipts ORDER BY id")]

    def delete(self, identity):
        with self.connect() as db:
            db.execute("DELETE FROM receipts WHERE id=?", (identity,))


class Ticket:
    def __init__(self, permit):
        self.permit = permit
        self.task = asyncio.current_task()
        self.handler_done = threading.Event()
        self.entered = False
        self.submitted = False
        self.prompt_id = None
        self.worker = None
        self.executor = None
        self.done = False
        self.removed = False
        self.receipt = False
        self.completing = False


class StorageWindows:
    """Persistent holds and closed IDs; no clock or lease can release a hold."""

    def __init__(self, receipts):
        self.receipts = receipts
        with receipts.connect() as db:
            db.execute("CREATE TABLE IF NOT EXISTS storage_identity (singleton INTEGER PRIMARY KEY CHECK(singleton=1), id TEXT NOT NULL)")
            db.execute("INSERT OR IGNORE INTO storage_identity(singleton,id) VALUES(1,?)", (secrets.token_hex(16),))
            db.execute("CREATE TABLE IF NOT EXISTS storage_windows (id TEXT PRIMARY KEY, request_hash TEXT NOT NULL, executor_id TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('held','closed')))")
            db.execute("CREATE UNIQUE INDEX IF NOT EXISTS storage_one_hold ON storage_windows(state) WHERE state='held'")
            self.identity = db.execute("SELECT id FROM storage_identity WHERE singleton=1").fetchone()[0]
            if not re.fullmatch(r"[0-9a-f]{32}", self.identity):
                raise RuntimeError("invalid storage journal identity")

    def current(self):
        with self.receipts.connect() as db:
            if db.execute("SELECT id FROM storage_identity WHERE singleton=1").fetchone() != (self.identity,):
                raise RuntimeError("storage journal changed")
            row = db.execute("SELECT id,request_hash,executor_id FROM storage_windows WHERE state='held'").fetchone()
            return dict(zip(("id", "request_hash", "executor_id"), row)) if row else None

    def backup(self):
        with self.receipts.connect() as db:
            return storage_snapshot(db, self.identity)

    def apply(self, action, identity, request_hash, executor_id, journal_id):
        with self.receipts.connect() as db:
            # The transaction also protects against a second cooperating process.
            db.execute("BEGIN IMMEDIATE")
            current_id = db.execute("SELECT id FROM storage_identity WHERE singleton=1").fetchone()
            if current_id != (journal_id,) or journal_id != self.identity:
                raise Rejected("storage journal changed")
            row = db.execute("SELECT request_hash,executor_id,state FROM storage_windows WHERE id=?", (identity,)).fetchone()
            if row and row[:2] != (request_hash, executor_id):
                raise Rejected("storage operation identity mismatch")
            state = row[2] if row else "unknown"
            if action == "hold" and state == "unknown":
                if db.execute("SELECT 1 FROM storage_windows WHERE state='held'").fetchone():
                    raise Rejected("storage window is busy")
                db.execute("INSERT INTO storage_windows VALUES(?,?,?,'held')", (identity, request_hash, executor_id))
                state = "held"
            elif action == "close":
                # Closing an unknown ID fences a hold that has not arrived yet.
                db.execute("INSERT INTO storage_windows VALUES(?,?,?,'closed') ON CONFLICT(id) DO UPDATE SET state='closed'", (identity, request_hash, executor_id))
                state = "closed"
            return {"protocol": 1, "journal_id": self.identity, "id": identity,
                    "request_hash": request_hash, "executor_id": executor_id, "state": state}


class Guard:
    def __init__(self, server, executor_class, quiesce):
        self.server, self.queue = server, server.prompt_queue
        self.executor_class, self.quiesce = executor_class, quiesce
        self.instance = secrets.token_hex(16)
        self.client = self.receipts = None
        self.active = None
        self.failure = None
        self.state = threading.RLock()
        self.admission = asyncio.Lock()
        self.delivery = asyncio.Lock()
        self.tasks = set()
        self.retry_task = None
        self.wrappers = {}
        self.originals = {}
        self.storage = None
        self.storage_enabled = False
        self.writers = 0

        @web.middleware
        async def middleware(request, handler):
            if request.method == "POST" and request.path in ("/prompt", "/api/prompt"):
                return await self.prompt(request, handler)
            if (self.storage_enabled and request.method not in ("GET", "HEAD", "OPTIONS") and
                    request.path != "/ai-gateway/storage/window"):
                return await self.mutation(request, handler)
            return await handler(request)

        self.middleware = middleware
        server.app.middlewares.append(middleware)
        server.routes.get("/ai-gateway/gpu/status")(self.status)
        server.routes.post("/ai-gateway/storage/window")(self.storage_window)
        server.routes.get("/ai-gateway/storage/backup")(self.storage_backup)
        server.app.on_startup.append(self.startup)
        server.app.on_cleanup.append(self.cleanup)
        self.install_queue_hooks()

    def configure(self, endpoint, token, receipt_db, storage_enabled=False):
        if type(storage_enabled) is not bool:
            raise ValueError("storage_enabled must be a boolean")
        self.storage_enabled = storage_enabled
        path = Path(receipt_db)
        if not path.is_absolute() or not path.parent.is_dir():
            raise ValueError("receipt_db must be an absolute path in an existing private directory")
        self.client = PermitClient(endpoint, token)
        try:
            self.receipts = Receipts(path)
        except Exception:
            self.storage_enabled = True
            self.fail()
            raise
        if storage_enabled:
            self.storage = StorageWindows(self.receipts)
        else:
            with self.receipts.connect() as db:
                exists = db.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name='storage_windows'").fetchone()
                if exists and db.execute("SELECT 1 FROM storage_windows WHERE state='held'").fetchone():
                    self.storage_enabled = True
                    self.fail()
                    raise ValueError("cannot disable storage guard with a held window")

    def fail(self):
        self.failure = "executor_guard_unconfirmed"
        LOG.error("GPU guard stopped admitting work; operator reconciliation is required")

    def healthy(self):
        return (self.failure is None and self.client is not None and self.receipts is not None and
                (not self.storage_enabled or self.storage is not None) and
                self.server.prompt_queue is self.queue and
                self.middleware in self.server.app.middlewares and
                all(getattr(self.queue, name, None) is wrapper for name, wrapper in self.wrappers.items()) and
                self.executor_class.execute is self.execute_wrapper)

    async def status(self, request):
        if self.client is None or not hmac.compare_digest(request.headers.get(KEY, "").encode("utf-8"), self.client.token.encode("ascii")):
            raise web.HTTPNotFound()
        held = self.storage_blocked()
        return web.json_response({"protocol": 1, "enforced": self.healthy(), "instance_id": self.instance,
                                  "storage_protocol": 1 if self.storage_enabled and self.storage else 0,
                                  "storage_held": held,
                                  "storage_journal_id": self.storage.identity if self.storage else ""},
                                 headers={"Cache-Control": "no-store"})

    def storage_blocked(self):
        if not self.storage_enabled:
            return False
        try:
            return self.storage is None or self.storage.current() is not None
        except Exception:
            self.fail()
            return True

    async def storage_backup(self, request):
        if (not self.storage_enabled or self.client is None or
                not hmac.compare_digest(request.headers.get(KEY, "").encode("utf-8"), self.client.token.encode("ascii"))):
            raise web.HTTPNotFound()
        headers = {"Cache-Control": "no-store"}
        if request.query_string:
            return web.json_response({"protocol": 1, "error": "invalid_storage_backup"}, status=400, headers=headers)
        if not self.healthy():
            return web.json_response({"protocol": 1, "error": "storage_guard_unavailable"}, status=503, headers=headers)
        try:
            data = await asyncio.to_thread(self.storage.backup)
            return web.json_response(data, headers=headers)
        except Exception:
            return web.json_response({"protocol": 1, "error": "storage_backup_unavailable"}, status=503, headers=headers)

    async def mutation(self, request, handler):
        with self.state:
            if not self.healthy():
                raise web.HTTPServiceUnavailable(text="File admission is unavailable")
            if self.storage_blocked():
                raise web.HTTPConflict(text="Storage maintenance is pending")
            self.writers += 1
        try:
            return await handler(request)
        except web.HTTPException as error:
            if error.status >= 500:
                self.fail()
            raise
        except BaseException:
            # A canceled handler may still own a threaded write. Only process
            # shutdown or operator reconciliation can prove it has stopped.
            self.fail()
            raise
        finally:
            with self.state:
                self.writers -= 1

    async def storage_window(self, request):
        if (not self.storage_enabled or self.client is None or
                not hmac.compare_digest(request.headers.get(KEY, "").encode("utf-8"), self.client.token.encode("ascii"))):
            raise web.HTTPNotFound()
        headers = {"Cache-Control": "no-store"}
        try:
            body = bytearray()
            async for chunk in request.content.iter_chunked(1024):
                body.extend(chunk)
                if len(body) > 2048:
                    raise ValueError("body too large")
            data = json.loads(body)
            required = {"protocol", "action", "id", "request_hash", "executor_id", "journal_id"}
            if not isinstance(data, dict) or data.get("action") not in ("read", "hold", "close"):
                raise ValueError("invalid operation")
            if data["action"] == "close":
                required.add("executor_fenced")
                if data.get("executor_fenced") is not True:
                    raise ValueError("executor fencing assertion required")
            if set(data) != required or type(data["protocol"]) is not int or data["protocol"] != 1:
                raise ValueError("invalid fields")
            for key, pattern in (("id", r"[A-Za-z0-9_-]{16,128}"), ("request_hash", r"[0-9a-f]{64}"),
                                 ("executor_id", r"[0-9a-f]{64}"), ("journal_id", r"[0-9a-f]{32}")):
                if not isinstance(data[key], str) or not re.fullmatch(pattern, data[key]):
                    raise ValueError("invalid identity")
        except (ValueError, UnicodeError):
            return web.json_response({"protocol": 1, "error": "invalid_storage_window"}, status=400, headers=headers)
        if not self.healthy():
            return web.json_response({"protocol": 1, "error": "storage_guard_unavailable"}, status=503, headers=headers)
        if self.admission.locked():
            return web.json_response({"protocol": 1, "error": "storage_guard_busy"}, status=409, headers=headers)
        async with self.admission:
            try:
                if data["action"] == "hold":
                    with self.state:
                        if self.active is not None or self.writers or self.receipts.all():
                            raise Rejected("active work or writes")
                    with self.queue.mutex:
                        if self.queue.queue or self.queue.currently_running:
                            raise Rejected("queue is not quiescent")
                result = self.storage.apply(data["action"], data["id"], data["request_hash"],
                                            data["executor_id"], data["journal_id"])
                return web.json_response(result, headers=headers)
            except Rejected:
                return web.json_response({"protocol": 1, "error": "storage_window_conflict"}, status=409, headers=headers)
            except Exception:
                self.fail()
                return web.json_response({"protocol": 1, "error": "storage_guard_unavailable"}, status=503, headers=headers)

    def spawn(self, coroutine):
        task = asyncio.create_task(coroutine)
        self.tasks.add(task)
        task.add_done_callback(self.tasks.discard)
        return task

    async def startup(self, app):
        if not self.healthy():
            raise RuntimeError("GPU guard is not configured or its hooks were replaced")
        self.retry_task = asyncio.create_task(self.retry())

    async def cleanup(self, app):
        if self.retry_task is not None:
            self.retry_task.cancel()
            await asyncio.gather(self.retry_task, return_exceptions=True)
        if self.tasks:
            await asyncio.gather(*list(self.tasks), return_exceptions=True)

    async def retry(self):
        while True:
            await self.deliver()
            await asyncio.sleep(3)

    async def deliver(self):
        async with self.delivery:
            try:
                for permit, code in self.receipts.all():
                    await self.client.call("finish", permit, code)
                    self.receipts.delete(permit["id"])
                    with self.state:
                        if self.active and self.active.permit["id"] == permit["id"]:
                            self.active = None
            except asyncio.CancelledError:
                raise
            except Exception:
                # Do not log payloads, keys, endpoint credentials, or prompts.
                LOG.warning("GPU completion receipt remains pending")

    def record(self, ticket, code):
        try:
            self.receipts.save(ticket.permit, code)
            ticket.receipt = True
            self.server.loop.call_soon_threadsafe(lambda: self.spawn(self.deliver()))
        except Exception:
            self.fail()

    async def complete_without_worker(self, ticket, entered):
        with self.state:
            if not self.healthy() or ticket is not self.active or ticket.completing or ticket.worker is not None:
                return
            ticket.completing = True
        try:
            if entered:
                await asyncio.to_thread(self.quiesce, None)
            if not self.healthy():
                raise RuntimeError("guard hooks changed")
            self.record(ticket, "quiesced" if entered else "not_started")
        except Exception:
            self.fail()

    async def prompt(self, request, handler):
        if not self.healthy():
            raise web.HTTPServiceUnavailable(text="GPU admission is unavailable")
        if self.admission.locked():
            raise web.HTTPConflict(text="GPU is busy", headers={"Retry-After": "3"})
        async with self.admission:
            if self.storage_blocked():
                raise web.HTTPConflict(text="Storage maintenance is pending")
            if self.active is not None or self.receipts.all():
                raise web.HTTPConflict(text="GPU completion is pending", headers={"Retry-After": "3"})
            private = {k.lower(): v for k, v in request.headers.items() if k.lower().startswith(PREFIX)}
            permit = {"id": secrets.token_hex(16), "instance_id": self.instance, "token": secrets.token_hex(32)}
            if private:
                if (set(private) != {KEY, WORK, LEASE} or
                        not hmac.compare_digest(private[KEY].encode("utf-8"), self.client.token.encode("ascii")) or
                        not private[WORK] or not private[LEASE]):
                    raise web.HTTPForbidden(text="Invalid managed GPU admission")
                permit.update(work_id=private[WORK], work_lease=private[LEASE])
            clean = request.headers.copy()
            for name in list(clean):
                if name.lower().startswith(PREFIX):
                    del clean[name]
            prepared = request.clone(headers=clean)
            # Parsing cannot execute nodes; reject malformed input before owning
            # a GPU permit. ComfyUI still performs its own domain validation.
            try:
                payload = await prepared.json()
            except (ValueError, UnicodeError):
                raise web.HTTPBadRequest(text="Invalid prompt JSON") from None
            if not isinstance(payload, dict):
                raise web.HTTPBadRequest(text="Prompt JSON must be an object")
            if not self.healthy():
                raise web.HTTPServiceUnavailable(text="GPU admission is unavailable")
            ticket = Ticket(permit)
            self.active = ticket
            reserved = False
            try:
                await self.client.call("reserve", permit)
                reserved = True
                await self.client.call("start", permit)
            except Rejected:
                if reserved:
                    self.record(ticket, "not_started")
                else:
                    self.active = None
                raise web.HTTPConflict(text="GPU admission deferred", headers={"Retry-After": "3"})
            except asyncio.CancelledError:
                self.record(ticket, "not_started")
                raise
            except Exception:
                self.record(ticket, "not_started")
                raise web.HTTPServiceUnavailable(text="GPU admission outcome is being reconciled") from None
            ticket.entered = True
            context_token = CURRENT.set(ticket)
            try:
                return await handler(prepared)
            except BaseException:
                # A canceled/failed validator may still own asynchronous work.
                # Its absence from the queue is not proof of quiescence.
                self.fail()
                raise
            finally:
                CURRENT.reset(context_token)
                ticket.handler_done.set()
                if not ticket.submitted or ticket.removed:
                    self.spawn(self.complete_without_worker(ticket, True))

    def install_queue_hooks(self):
        q = self.queue
        for name in ("put", "get", "task_done", "wipe_queue", "delete_queue_item"):
            if not callable(getattr(q, name, None)):
                raise RuntimeError("unsupported ComfyUI queue")
            self.originals[name] = getattr(q, name)
        if not hasattr(q, "mutex") or not isinstance(q.queue, list) or not isinstance(q.currently_running, dict):
            raise RuntimeError("unsupported ComfyUI queue state")
        if q.queue or q.currently_running:
            raise RuntimeError("cannot attach guard to an active queue")
        original_execute = self.executor_class.execute

        def put(item):
            with self.state:
                ticket = CURRENT.get()
                try:
                    task = asyncio.current_task()
                except RuntimeError:
                    task = None
                if (not self.healthy() or self.storage_blocked() or ticket is None or ticket is not self.active or
                        ticket.task is not task or not ticket.entered or
                        ticket.handler_done.is_set() or ticket.submitted):
                    raise RuntimeError("queue submission has no GPU permit")
                if not isinstance(item, (tuple, list)) or len(item) < 5 or not isinstance(item[1], str) or not item[1]:
                    raise RuntimeError("unsupported prompt queue item")
                ticket.submitted, ticket.prompt_id = True, item[1]
                return self.originals["put"](item)

        def get(*args, **kwargs):
            ticket = self.active
            if ticket and ticket.worker == threading.get_ident() and ticket.done and not ticket.completing:
                ticket.completing = True
                # The worker has finished post-history output handling before its
                # next get. Only now may its executor caches be reset.
                ticket.handler_done.wait()
                try:
                    if not self.healthy() or ticket.executor is None:
                        raise RuntimeError("executor ownership changed")
                    self.quiesce(ticket.executor)
                    self.record(ticket, "quiesced")
                except Exception:
                    self.fail()
            result = self.originals["get"](*args, **kwargs)
            if result is not None:
                with self.state:
                    ticket = self.active
                    if (not self.healthy() or self.storage_blocked() or ticket is None or ticket.worker is not None or
                            not ticket.submitted or ticket.prompt_id != result[0][1] or ticket.removed):
                        self.fail()
                        raise RuntimeError("unadmitted queue execution")
                    ticket.worker = threading.get_ident()
            return result

        def execute(executor, prompt, prompt_id, *args, **kwargs):
            ticket = self.active
            if (not self.healthy() or self.storage_blocked() or ticket is None or ticket.worker != threading.get_ident() or
                    ticket.prompt_id != prompt_id or ticket.executor is not None or ticket.done):
                self.fail()
                raise RuntimeError("executor has no GPU permit")
            ticket.executor = executor
            return original_execute(executor, prompt, prompt_id, *args, **kwargs)

        def task_done(item_id, *args, **kwargs):
            with q.mutex:
                ticket = self.active
                item = q.currently_running.get(item_id)
                if (ticket is None or ticket.worker != threading.get_ident() or ticket.executor is None or
                        item is None or item[1] != ticket.prompt_id or ticket.done):
                    self.fail()
                    raise RuntimeError("unowned executor completion")
                result = self.originals["task_done"](item_id, *args, **kwargs)
                ticket.done = True
                return result

        def remove(name, *args, **kwargs):
            with q.mutex:
                before = {item[1] for item in q.queue}
                result = self.originals[name](*args, **kwargs)
                after = {item[1] for item in q.queue}
                ticket = self.active
                if (ticket and ticket.prompt_id in before - after and ticket.worker is None and not ticket.removed):
                    ticket.removed = True
                    if ticket.handler_done.is_set():
                        self.server.loop.call_soon_threadsafe(lambda: self.spawn(self.complete_without_worker(ticket, True)))
                return result

        self.execute_wrapper = execute
        self.executor_class.execute = execute
        self.wrappers = {"put": put, "get": get, "task_done": task_done,
                         "wipe_queue": lambda: remove("wipe_queue"),
                         "delete_queue_item": lambda function: remove("delete_queue_item", function)}
        for name, wrapper in self.wrappers.items():
            setattr(q, name, wrapper)


def cuda_quiescer(torch, model_management):
    def quiesce(executor):
        import gc

        if not torch.cuda.is_available() or torch.cuda.device_count() < 1:
            raise RuntimeError("this adapter requires the verified CUDA runtime")
        for device in range(torch.cuda.device_count()):
            torch.cuda.synchronize(device)
        if executor is not None:
            executor.reset()
        model_management.unload_all_models()
        gc.collect()
        model_management.soft_empty_cache()
        for device in range(torch.cuda.device_count()):
            torch.cuda.synchronize(device)
            if torch.cuda.memory_allocated(device) != 0 or torch.cuda.memory_reserved(device) != 0:
                raise RuntimeError("executor retains live CUDA tensors")
    return quiesce
