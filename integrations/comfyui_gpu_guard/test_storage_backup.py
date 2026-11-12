import asyncio
import copy
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

import storage_backup
import test_guard


class StorageBackupTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.receipts = test_guard.module.Receipts(self.root / "source.sqlite")
        self.storage = test_guard.module.StorageWindows(self.receipts)

    def data(self):
        self.storage.apply("close", "closed-operation-0001", "a" * 64, "b" * 64, self.storage.identity)
        self.storage.apply("hold", "held-operation-00001", "c" * 64, "b" * 64, self.storage.identity)
        self.receipts.save({"id": "d" * 32, "instance_id": "e" * 32, "token": "f" * 64,
                            "work_id": "work-id", "work_lease": "old-work-lease"}, "quiesced")
        return self.storage.backup()

    def test_roundtrip_preserves_closed_ids_hold_and_gpu_receipts(self):
        data = self.data()
        destination = self.root / "restored.sqlite"
        storage_backup.restore(data, destination)
        with self.assertRaisesRegex(RuntimeError, "isolated restored"):
            test_guard.module.Receipts(destination)
        with closing(sqlite3.connect(destination)) as db:
            windows = [dict(zip(("id", "request_hash", "executor_id", "state"), row)) for row in
                       db.execute("SELECT id,request_hash,executor_id,state FROM storage_windows ORDER BY id")]
            receipts = [{"id": row[0], "payload": json.loads(row[1]), "code": row[2]} for row in
                        db.execute("SELECT id,payload,code FROM receipts ORDER BY id")]
            self.assertEqual(windows, data["windows"])
            self.assertEqual(receipts, data["receipts"])
            self.assertEqual(db.execute("SELECT id FROM storage_identity").fetchone(), (data["journal_id"],))
            self.assertEqual(db.execute("SELECT reason FROM isolated_restore_blocked").fetchone(), ("isolated_restore_only",))

    def test_does_not_overwrite_existing_database_or_follow_symlink(self):
        data = self.data()
        existing = self.root / "existing.sqlite"
        existing.write_bytes(b"private existing bytes")
        with self.assertRaises(FileExistsError):
            storage_backup.restore(data, existing)
        self.assertEqual(existing.read_bytes(), b"private existing bytes")
        linked = self.root / "linked.sqlite"
        try:
            linked.symlink_to(existing)
        except OSError:
            return
        with self.assertRaises(FileExistsError):
            storage_backup.restore(data, linked)
        self.assertEqual(existing.read_bytes(), b"private existing bytes")

    def test_invalid_payloads_never_publish_database(self):
        original = self.data()
        for mode in ("protocol", "identity", "duplicate", "two_holds", "token", "code", "unknown", "empty_lease", "order"):
            with self.subTest(mode=mode):
                data = copy.deepcopy(original)
                if mode == "protocol":
                    data["protocol"] = True
                elif mode == "identity":
                    data["journal_id"] = "bad"
                elif mode == "duplicate":
                    data["windows"].append(data["windows"][0])
                elif mode == "two_holds":
                    data["windows"][0]["state"] = "held"
                elif mode == "token":
                    data["receipts"][0]["payload"]["token"] = "bad"
                elif mode == "code":
                    data["receipts"][0]["code"] = "timeout"
                elif mode == "unknown":
                    data["tables"] = []
                elif mode == "empty_lease":
                    data["receipts"][0]["payload"]["work_lease"] = ""
                else:
                    data["windows"].reverse()
                destination = self.root / (mode + ".sqlite")
                with self.assertRaises(ValueError):
                    storage_backup.restore(data, destination)
                self.assertFalse(destination.exists())

    def test_snapshot_rejects_unknown_schema_and_changed_identity(self):
        with self.receipts.connect() as db:
            db.execute("CREATE TABLE future_schema(value TEXT)")
        with self.assertRaises(ValueError):
            self.storage.backup()
        with self.receipts.connect() as db:
            db.execute("DROP TABLE future_schema")
            db.execute("UPDATE storage_identity SET id=?", ("9" * 32,))
        with self.assertRaises(ValueError):
            self.storage.backup()

    def test_duplicate_json_fields_rejected(self):
        with self.assertRaises(ValueError):
            json.loads('{"protocol":1,"protocol":1}', object_pairs_hook=storage_backup.unique_object)

    def test_snapshot_keeps_valid_empty_journal(self):
        data = self.storage.backup()
        self.assertEqual(data["windows"], [])
        self.assertEqual(data["receipts"], [])
        destination = self.root / "empty.sqlite"
        storage_backup.restore(data, destination)
        with closing(sqlite3.connect(destination)) as db:
            self.assertEqual(db.execute("PRAGMA quick_check").fetchone(), ("ok",))

    def test_restored_guard_refuses_configuration_even_when_storage_disabled(self):
        data = self.storage.backup()
        destination = self.root / "blocked.sqlite"
        storage_backup.restore(data, destination)
        loop = asyncio.new_event_loop()
        self.addCleanup(loop.close)
        for enabled in (True, False):
            with self.subTest(storage_enabled=enabled):
                server, executor = test_guard.fixture(loop)
                guard = test_guard.module.Guard(server, executor, lambda _: None)
                with self.assertRaisesRegex(RuntimeError, "isolated restored"):
                    guard.configure("http://127.0.0.1:1/internal/gpu/comfy/permit", "k" * 64,
                                    str(destination), storage_enabled=enabled)
                self.assertFalse(guard.healthy())
                self.assertTrue(guard.storage_enabled)
                self.assertTrue(guard.storage_blocked())


if __name__ == "__main__":
    unittest.main()
