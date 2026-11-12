import subprocess
import sys
import threading
import time

import numpy as np
import pytest

from .test_dlss5 import fg
from rh_dlss5_under_test.dlss5nr.lifecycle import WorkerGuard


class FastGuard(WorkerGuard):
    def operation(self, timeout):
        return super().operation(min(timeout, 0.5))


@pytest.mark.parametrize("phase", ["probe", "handshake", "frame", "write"])
def test_fg_blocked_worker_is_bounded(monkeypatch, tmp_path, phase):
    code = "import time; time.sleep(90)"
    if phase in ("frame", "write"):
        code = "import sys,struct,time; sys.stdin.buffer.read(20); sys.stdout.buffer.write(struct.pack('<4I',0x31524746,0,1,0)); sys.stdout.buffer.flush(); "
        if phase == "frame": code += "sys.stdin.buffer.read(32+512*512*8); "
        code += "time.sleep(90)"
    monkeypatch.setattr(fg, "_worker_command", lambda *args: [sys.executable, "-u", "-c", code])
    monkeypatch.setattr(fg, "_worker_env", lambda *args: None)
    monkeypatch.setattr(fg, "WorkerGuard", FastGuard)
    workers = []
    start = subprocess.Popen
    def capture(*args, **kwargs):
        worker = start(*args, **kwargs)
        workers.append(worker)
        return worker
    monkeypatch.setattr(fg.subprocess, "Popen", capture)
    session = None
    started = time.monotonic()
    try:
        with pytest.raises(TimeoutError):
            if phase == "probe":
                for name in fg.RUNTIME_FILES: (tmp_path / name).touch()
                fg.probe_fg_capabilities(tmp_path)
            else:
                session = fg.FGSession(tmp_path, 512, 512, 2, 1)
                session.process_frame(np.zeros((512, 512, 4), np.uint8), np.zeros((512, 512, 2), np.float16), fg.Fraction(0), True, 0)
    finally:
        if session:
            try: session.close()
            except TimeoutError: pass
        for worker in workers:
            assert worker.poll() is not None
            for pipe in (worker.stdin, worker.stdout, worker.stderr):
                if pipe: pipe.close()
    assert time.monotonic() - started < 4


def test_fg_cancel_during_handshake(monkeypatch, tmp_path):
    event = threading.Event()
    monkeypatch.setattr(fg, "_worker_command", lambda *args: [sys.executable, "-u", "-c", "import time; time.sleep(90)"])
    monkeypatch.setattr(fg, "_worker_env", lambda *args: None)
    monkeypatch.setattr(fg, "WorkerGuard", lambda worker: WorkerGuard(worker, event.is_set))
    timer = threading.Timer(0.2, event.set)
    timer.start()
    started = time.monotonic()
    try:
        with pytest.raises(InterruptedError): fg.FGSession(tmp_path, 64, 64, 2, 1)
    finally:
        timer.join()
    assert time.monotonic() - started < 3
