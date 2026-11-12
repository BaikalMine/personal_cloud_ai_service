"""HTTP fixture for the Go/PostgreSQL protocol test. Never imports ComfyUI."""

import asyncio
from pathlib import Path
import sys

from aiohttp import web
from aiohttp.test_utils import TestServer

from test_guard import fixture, module


async def main():
    server, executor_class = fixture(asyncio.get_running_loop())

    def quiesce(executor):
        if executor:
            executor.reset()

    guard = module.Guard(server, executor_class, quiesce)
    guard.configure(sys.argv[1], sys.argv[2], Path(sys.argv[3]))

    async def prompt(request):
        payload = await request.json()
        if any(name.lower().startswith(module.PREFIX) for name in request.headers):
            raise RuntimeError("private header reached executor")
        server.prompt_queue.put((0, "fixture-prompt", payload, {}, []))
        return web.json_response({"prompt_id": "fixture-prompt"})

    def worker():
        q = server.prompt_queue
        item, identity = q.get()
        executor = executor_class()
        executor.execute(item[2], item[1], item[3], item[4])
        q.task_done(identity, executor.history_result)
        assert executor.reset_count == 0
        assert executor.history_result is not None
        q.get()
        assert executor.reset_count == 1

    async def run_worker(request):
        await asyncio.to_thread(worker)
        await guard.deliver()
        return web.json_response({"closed": guard.active is None})

    async def queue(request):
        q = server.prompt_queue
        with q.mutex:
            return web.json_response({"queue_running": list(q.currently_running.values()), "queue_pending": q.queue})

    async def history(request):
        return web.json_response(server.prompt_queue.history)

    server.routes.post("/prompt")(prompt)
    server.routes.get("/queue")(queue)
    server.routes.post("/test/run")(run_worker)
    server.routes.get("/test/history")(history)
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
