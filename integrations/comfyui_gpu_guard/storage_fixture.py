"""Synthetic storage protocol peer for Go tests; never imports ComfyUI or CUDA."""

import asyncio
from pathlib import Path
import sys

from aiohttp import web
from aiohttp.test_utils import TestServer

from test_guard import FakeClient, fixture, module


async def main():
    state = Path(sys.argv[1])
    server, executor_class = fixture(asyncio.get_running_loop())
    guard = module.Guard(server, executor_class, lambda executor: executor.reset() if executor else None)
    guard.configure("http://127.0.0.1:9/internal/gpu/comfy/permit", "k" * 64,
                    state / "guard.sqlite", storage_enabled=True)
    guard.client = FakeClient()

    async def prompt(request):
        server.prompt_queue.put((0, "fixture-prompt", await request.json(), {}, []))
        return web.json_response({"prompt_id": "fixture-prompt"})

    async def upload(request):
        payload = await request.read()
        (state / "upload-reached-handler").write_bytes(payload)
        return web.json_response({"ok": True})

    def worker():
        item, identity = server.prompt_queue.get()
        executor = executor_class()
        executor.execute(item[2], item[1], item[3], item[4])
        server.prompt_queue.task_done(identity, executor.history_result)
        server.prompt_queue.get()

    async def complete(request):
        await asyncio.to_thread(worker)
        await guard.deliver()
        return web.json_response({"closed": guard.active is None})

    server.routes.post("/prompt")(prompt)
    server.routes.post("/api/prompt")(prompt)
    server.routes.post("/upload/image")(upload)
    server.routes.post("/api/upload/mask")(upload)
    server.routes.post("/test/complete")(complete)
    server.app.add_routes(server.routes)
    http = TestServer(server.app)
    await http.start_server()
    print(str(http.make_url("")), flush=True)
    try:
        await asyncio.Event().wait()
    finally:
        await http.close()


if __name__ == "__main__":
    asyncio.run(main())
