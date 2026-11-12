import subprocess
import sys
import threading
import time
import unittest

from lifecycle import WorkerGuard


class WorkerGuardTest(unittest.TestCase):
    def worker(self, code):
        process = subprocess.Popen([sys.executable, "-u", "-c", code], stdin=subprocess.PIPE,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
        self.addCleanup(lambda: [pipe.close() for pipe in (process.stdin, process.stdout, process.stderr)])
        self.addCleanup(lambda: process.wait(timeout=5))
        self.addCleanup(lambda: process.kill() if process.poll() is None else None)
        return process

    def test_hung_read_has_deadline(self):
        process = self.worker("import time; time.sleep(90)")
        guard = WorkerGuard(process)
        started = time.monotonic()
        try:
            with self.assertRaises(TimeoutError), guard.operation(0.2):
                process.stdout.read(1)
        finally:
            guard.close()
        self.assertLess(time.monotonic() - started, 3)
        self.assertIsNotNone(process.poll())

    def test_hung_write_has_deadline(self):
        process = self.worker("import time; time.sleep(90)")
        guard = WorkerGuard(process)
        try:
            with self.assertRaises(TimeoutError), guard.operation(0.2):
                process.stdin.write(b"x" * (2 * 1024 * 1024))
                process.stdin.flush()
        finally:
            guard.close()

    def test_cancel_during_blocked_read(self):
        process = self.worker("import time; time.sleep(90)")
        event = threading.Event()
        guard = WorkerGuard(process, event.is_set)
        timer = threading.Timer(0.2, event.set)
        timer.start()
        try:
            with self.assertRaises(InterruptedError), guard.operation(10):
                process.stdout.read(1)
        finally:
            guard.close()
            timer.join()
        self.assertIsNotNone(process.poll())

    def test_successful_io_and_idempotent_close(self):
        process = self.worker("print('ok', flush=True)")
        guard = WorkerGuard(process)
        with guard.operation(5):
            self.assertEqual(process.stdout.readline().strip(), b"ok")
        guard.close()
        guard.close()

    def test_close_terminates_idle_worker(self):
        process = self.worker("import time; time.sleep(90)")
        guard = WorkerGuard(process)
        guard.close()
        self.assertIsNotNone(process.poll())


if __name__ == "__main__":
    unittest.main()
