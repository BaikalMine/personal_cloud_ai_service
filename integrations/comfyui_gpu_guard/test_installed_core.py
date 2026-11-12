"""Opt-in CPU-only test against an archived installed ComfyUI core."""

import argparse
import asyncio
import importlib.metadata
import importlib.util
import json
import os
from pathlib import Path
import sys
import threading
import time


def load_core():
    parser = argparse.ArgumentParser()
    parser.add_argument("--core", type=Path, required=True)
    parser.add_argument("--state", type=Path, required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--cache-mode", choices=("default", "none"), default="default")
    options = parser.parse_args()
    core, state = options.core.resolve(), options.state.resolve()
    if not core.is_dir() or not state.is_dir() or core == Path("C:/Work/ComfyUI"):
        raise RuntimeError("a disposable archived core and state directory are required")
    if os.environ.get("CUDA_VISIBLE_DEVICES") != "-1":
        raise RuntimeError("CUDA must be hidden before starting this test process")
    sys.dont_write_bytecode = True
    sys.path.insert(0, str(core))
    sys.argv = [str(core / "main.py"), "--cpu", "--disable-dynamic-vram",
                "--disable-all-custom-nodes", "--disable-api-nodes",
                "--base-directory", str(state), "--front-end-root", str(state),
                "--database-url", "sqlite:///:memory:", "--verbose", "ERROR"]
    if options.cache_mode == "none":
        sys.argv.append("--cache-none")
    os.chdir(core)
    import main
    import torch
    if torch.cuda.is_initialized() or main.comfy.model_management.get_torch_device().type != "cpu":
        raise RuntimeError("test core did not remain CPU-only")
    spec = importlib.util.spec_from_file_location("gateway_core_test_guard", Path(__file__).with_name("guard.py"))
    guard = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(guard)
    try:
        guard.cuda_quiescer(torch, main.comfy.model_management)(None)
    except RuntimeError as error:
        assert str(error) == "this adapter requires the verified CUDA runtime"
    else:
        raise AssertionError("production CUDA quiescer accepted CPU-only evidence")
    return options, main, torch, guard


async def eventually(predicate, description):
    deadline = time.monotonic() + 10
    while not predicate():
        if time.monotonic() >= deadline:
            raise AssertionError(description)
        await asyncio.sleep(0.02)


async def run(options, core, torch, adapter):
    from aiohttp import web
    from aiohttp.test_utils import TestClient, TestServer

    key = "synthetic-comfy-core-test-key-" + "x" * 32
    held = {}
    actions = []
    reject_next = False

    async def permit(request):
        nonlocal reject_next
        assert request.headers.get("Authorization") == "Bearer " + key
        data = await request.json()
        action, identity = data["action"], data["id"]
        actions.append((action, identity, data.get("code")))
        if action == "reserve":
            if reject_next or held:
                reject_next = False
                return web.json_response({"protocol": 1, "error": "executor_permit_unavailable"}, status=409)
            held[identity] = False
            return web.json_response({"protocol": 1, "id": identity, "state": "reserved"})
        if action == "start":
            assert identity in held and not held[identity]
            held[identity] = True
            return web.json_response({"protocol": 1, "id": identity, "started": True})
        assert action == "finish" and identity in held
        assert data["code"] in ("not_started", "quiesced")
        del held[identity]
        return web.json_response({"protocol": 1, "id": identity, "state": "closed"})

    callback_app = web.Application()
    callback_app.router.add_post("/internal/gpu/comfy/permit", permit)
    callback = TestServer(callback_app, host="127.0.0.1")
    await callback.start_server()
    prompt_server = core.server.PromptServer(asyncio.get_running_loop())
    node_started, node_release = threading.Event(), threading.Event()
    executions = []
    quiesced = []
    fail_quiesce = False

    class FixtureOutput:
        @classmethod
        def INPUT_TYPES(cls):
            return {"required": {"value": ("STRING", {"default": "fixture"})}}

        RETURN_TYPES = ()
        OUTPUT_NODE = True
        FUNCTION = "run"
        CATEGORY = "test"

        def run(self, value):
            assert held and all(held.values()), "node executed before confirmed admission"
            assert not torch.cuda.is_initialized(), "fixture initialized CUDA"
            executions.append(value)
            node_started.set()
            deadline = time.monotonic() + 15
            while not node_release.wait(0.02):
                core.comfy.model_management.throw_exception_if_processing_interrupted()
                if time.monotonic() >= deadline:
                    raise RuntimeError("fixture release did not arrive")
            return {"ui": {"text": [value]}, "result": ()}

    core.nodes.NODE_CLASS_MAPPINGS = {"GatewayCoreFixture": FixtureOutput}

    def quiesce(executor):
        if fail_quiesce:
            raise RuntimeError("synthetic stop proof failure")
        assert not torch.cuda.is_initialized()
        if executor is not None:
            history = prompt_server.prompt_queue.get_history()
            assert history, "quiesced before native history publication"
            assert not prompt_server.prompt_queue.currently_running
            executor.reset()
        quiesced.append(executor is not None)

    guard = adapter.Guard(prompt_server, core.execution.PromptExecutor, quiesce)
    guard.configure(str(callback.make_url("/internal/gpu/comfy/permit")), key,
                    str(options.state.resolve() / "receipts.sqlite"))
    prompt_server.add_routes()
    client = TestClient(TestServer(prompt_server.app, host="127.0.0.1"))
    await client.start_server()

    async def request(method, path, expected, **kwargs):
        response = await client.request(method, path, **kwargs)
        body = await response.text()
        assert response.status == expected, (method, path, response.status, body[:500])
        if response.content_type == "application/json":
            return json.loads(body)
        return body

    def payload(value):
        return {"prompt": {"1": {"class_type": "GatewayCoreFixture", "inputs": {"value": value}}}}

    async def idle():
        await eventually(lambda: not held and guard.active is None, "terminal receipt did not release fixture permit")
        assert guard.healthy()

    try:
        await request("GET", "/ai-gateway/gpu/status", 404)
        status = await request("GET", "/ai-gateway/gpu/status", 200, headers={adapter.KEY: key})
        assert status["enforced"] and not actions
        await request("POST", "/prompt", 400, data="{broken", headers={"Content-Type": "application/json"})
        await request("POST", "/prompt", 403, json=payload("forged"), headers={adapter.KEY: "wrong"})
        assert not actions and not executions

        reject_next = True
        await request("POST", "/api/prompt", 409, json=payload("rejected"))
        assert not held and not executions and prompt_server.prompt_queue.get_tasks_remaining() == 0
        await request("POST", "/prompt", 400, json={})
        await idle()
        assert quiesced == [False]

        for cancel in ("delete", "clear", "job", "batch"):
            result = await request("POST", "/prompt", 200, json=payload("queued-" + cancel))
            assert held and not executions
            if cancel in ("delete", "clear"):
                body = {"delete": [result["prompt_id"]]} if cancel == "delete" else {"clear": True}
                await request("POST", "/queue", 200, json=body)
            else:
                path = "/api/jobs/" + result["prompt_id"] + "/cancel" if cancel == "job" else "/api/jobs/cancel"
                response = await request("POST", path, 200, json={"job_ids": [result["prompt_id"]]})
                assert response["cancelled"]
            await idle()
            assert prompt_server.prompt_queue.get_tasks_remaining() == 0

        # Run the actual installed worker, including sensitive-data handling,
        # history/status publication and its post-execution maintenance segment.
        worker = threading.Thread(target=core.prompt_worker,
                                  args=(prompt_server.prompt_queue, prompt_server), daemon=True)
        worker.start()
        result = await request("POST", "/api/prompt", 200, json=payload("completed"))
        await eventually(node_started.is_set, "native worker did not execute fixture")
        assert held and not prompt_server.prompt_queue.get_history(result["prompt_id"])
        await request("POST", "/prompt", 409, json=payload("overlap"))
        try:
            prompt_server.prompt_queue.put((0, "unadmitted", {}, {}, [], {}))
        except RuntimeError:
            pass
        else:
            raise AssertionError("direct queue bypass accepted")
        node_release.set()
        await idle()
        history = await request("GET", "/history/" + result["prompt_id"], 200)
        assert history[result["prompt_id"]]["status"]["completed"]
        assert history[result["prompt_id"]]["outputs"]["1"]["text"] == ["completed"]
        assert executions == ["completed"] and quiesced[-1]

        for cancel in ("interrupt", "job", "batch"):
            node_release.clear()
            node_started.clear()
            result = await request("POST", "/prompt", 200, json=payload("interrupted-" + cancel))
            await eventually(node_started.is_set, "interrupt fixture did not start")
            path = {"interrupt": "/interrupt", "job": "/api/jobs/" + result["prompt_id"] + "/cancel",
                    "batch": "/api/jobs/cancel"}[cancel]
            await request("POST", path, 200, json={"job_ids": [result["prompt_id"]]})
            await idle()
            history = await request("GET", "/history/" + result["prompt_id"], 200)
            assert not history[result["prompt_id"]]["status"]["completed"]

        fail_quiesce = True
        node_started.clear()
        node_release.set()
        await request("POST", "/prompt", 200, json=payload("unconfirmed-stop"))
        await eventually(lambda: guard.failure is not None, "lost stop proof did not close admission")
        assert held and guard.active is not None
        await request("POST", "/api/prompt", 503, json=payload("must-not-start"))
        status = await request("GET", "/ai-gateway/gpu/status", 200, headers={adapter.KEY: key})
        assert not status["enforced"]
        assert executions == ["completed", "interrupted-interrupt", "interrupted-job", "interrupted-batch", "unconfirmed-stop"]
        assert not torch.cuda.is_initialized()
        print(json.dumps({"result": "passed", "core_commit": options.commit,
                          "core_version": core.comfyui_version.__version__,
                          "python": sys.version.split()[0], "torch": torch.__version__,
                          "aiohttp": importlib.metadata.version("aiohttp"),
                          "cache_mode": options.cache_mode,
                          "fixture_executions": len(executions), "quiesced": len(quiesced),
                          "cuda_initialized": False, "real_gpu_stop_verified": False}))
    finally:
        node_release.set()
        await client.close()
        await callback.close()


if __name__ == "__main__":
    options, core, torch, adapter = load_core()
    asyncio.run(run(options, core, torch, adapter))
