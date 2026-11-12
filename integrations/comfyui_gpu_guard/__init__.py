"""Repository-only adapter; installation requires coordinated operator opt-in."""

import json
from pathlib import Path

import execution
import server
import torch
import comfy.model_management

from .guard import Guard, cuda_quiescer


NODE_CLASS_MAPPINGS = {}
NODE_DISPLAY_NAME_MAPPINGS = {}

# Install rejecting hooks before reading configuration. ComfyUI catches custom
# node import errors; an invalid config must leave admission closed, not bypassed.
GPU_GUARD = Guard(server.PromptServer.instance, execution.PromptExecutor,
                  cuda_quiescer(torch, comfy.model_management))
try:
    config = json.loads(Path(__file__).with_name("guard.json").read_text(encoding="utf-8"))
    if set(config) not in ({"endpoint", "token", "receipt_db"}, {"endpoint", "token", "receipt_db", "storage_enabled"}):
        raise ValueError("invalid GPU guard configuration keys")
    GPU_GUARD.configure(**config)
except Exception:
    GPU_GUARD.fail()
    raise RuntimeError("GPU guard configuration failed; admission remains closed") from None
