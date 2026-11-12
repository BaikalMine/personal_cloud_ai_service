"""Bound native worker I/O and tie its Windows descendants to the host lifetime."""
from contextlib import contextmanager
import ctypes
from ctypes import wintypes
import os
import subprocess
import sys
import threading
import time


class _BasicLimits(ctypes.Structure):
    _fields_ = [("process_time", ctypes.c_int64), ("job_time", ctypes.c_int64),
                ("flags", wintypes.DWORD), ("min_ws", ctypes.c_size_t),
                ("max_ws", ctypes.c_size_t), ("active", wintypes.DWORD),
                ("affinity", ctypes.c_size_t), ("priority", wintypes.DWORD),
                ("scheduling", wintypes.DWORD)]


class _IOCounters(ctypes.Structure):
    _fields_ = [(name, ctypes.c_uint64) for name in
                ("read_ops", "write_ops", "other_ops", "read_bytes", "write_bytes", "other_bytes")]


class _Limits(ctypes.Structure):
    _fields_ = [("basic", _BasicLimits), ("io", _IOCounters),
                ("process_memory", ctypes.c_size_t), ("job_memory", ctypes.c_size_t),
                ("peak_process_memory", ctypes.c_size_t), ("peak_job_memory", ctypes.c_size_t)]


class WindowsJob:
    def __init__(self, worker):
        self.kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        self.kernel.CreateJobObjectW.argtypes = [ctypes.c_void_p, wintypes.LPCWSTR]
        self.kernel.CreateJobObjectW.restype = wintypes.HANDLE
        self.kernel.SetInformationJobObject.argtypes = [wintypes.HANDLE, ctypes.c_int, ctypes.c_void_p, wintypes.DWORD]
        self.kernel.SetInformationJobObject.restype = wintypes.BOOL
        self.kernel.AssignProcessToJobObject.argtypes = [wintypes.HANDLE, wintypes.HANDLE]
        self.kernel.AssignProcessToJobObject.restype = wintypes.BOOL
        self.kernel.TerminateJobObject.argtypes = [wintypes.HANDLE, wintypes.UINT]
        self.kernel.TerminateJobObject.restype = wintypes.BOOL
        self.kernel.CloseHandle.argtypes = [wintypes.HANDLE]
        self.kernel.CloseHandle.restype = wintypes.BOOL
        self.handle = self.kernel.CreateJobObjectW(None, None)
        if not self.handle:
            raise ctypes.WinError(ctypes.get_last_error())
        try:
            limits = _Limits()
            limits.basic.flags = 0x2000
            if not self.kernel.SetInformationJobObject(self.handle, 9, ctypes.byref(limits), ctypes.sizeof(limits)):
                raise ctypes.WinError(ctypes.get_last_error())
            if not self.kernel.AssignProcessToJobObject(self.handle, wintypes.HANDLE(int(worker._handle))):
                raise ctypes.WinError(ctypes.get_last_error())
        except BaseException:
            self.close()
            raise

    def terminate(self):
        if self.handle and not self.kernel.TerminateJobObject(self.handle, 1):
            raise ctypes.WinError(ctypes.get_last_error())

    def close(self):
        if self.handle:
            self.kernel.CloseHandle(self.handle)
            self.handle = None


def _comfy_cancelled():
    module = sys.modules.get("comfy.model_management")
    return bool(module and module.processing_interrupted())


class WorkerGuard:
    def __init__(self, worker, cancelled=_comfy_cancelled):
        self.worker = worker
        self.cancelled = cancelled
        self.deadline = None
        self.failure = None
        self.stop = threading.Event()
        self.job = None
        self.thread = None
        try:
            if os.name == "nt":
                self.job = WindowsJob(worker)
            self.thread = threading.Thread(target=self._watch, name="dlss5-worker-watchdog", daemon=True)
            self.thread.start()
        except BaseException:
            worker.kill()
            worker.wait(timeout=5)
            if self.job:
                self.job.close()
            raise

    def _kill(self):
        if self.job:
            self.job.terminate()
        elif self.worker.poll() is None:
            self.worker.kill()

    def _watch(self):
        while not self.stop.wait(0.05):
            if self.worker.poll() is not None:
                return
            try:
                if self.cancelled():
                    self.failure = InterruptedError("DLSS cancelled")
                elif self.deadline is not None and time.monotonic() >= self.deadline:
                    self.failure = TimeoutError("Native DLSS worker exceeded its operation deadline")
                if self.failure:
                    self._kill()
                    return
            except BaseException as error:
                self.failure = error
                try:
                    self.worker.kill()
                except OSError:
                    pass
                return

    def _raise_failure(self):
        if isinstance(self.failure, InterruptedError):
            module = sys.modules.get("comfy.model_management")
            if module:
                module.throw_exception_if_processing_interrupted()
        if self.failure:
            raise self.failure

    @contextmanager
    def operation(self, timeout):
        self._raise_failure()
        self.deadline = time.monotonic() + timeout
        try:
            yield
        except BaseException:
            self._raise_failure()
            raise
        finally:
            self.deadline = None
        self._raise_failure()

    def close(self):
        self.stop.set()
        if self.thread and self.thread is not threading.current_thread():
            self.thread.join(timeout=2)
        try:
            if self.worker.poll() is None:
                self._kill()
            self.worker.wait(timeout=5)
        finally:
            if self.job:
                self.job.close()
