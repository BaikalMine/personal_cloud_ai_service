"""Logical guard backup and isolated SQLite restore, using only the standard library."""

import json
import os
from pathlib import Path
import re
import sqlite3
import sys


MAX_BYTES = 64 << 20
MAX_ROWS = 65536
SCHEMA = (
    "CREATE TABLE receipts (id TEXT PRIMARY KEY, payload TEXT NOT NULL, code TEXT NOT NULL CHECK(code IN ('not_started','quiesced')))",
    "CREATE TABLE storage_identity (singleton INTEGER PRIMARY KEY CHECK(singleton=1), id TEXT NOT NULL)",
    "CREATE TABLE storage_windows (id TEXT PRIMARY KEY, request_hash TEXT NOT NULL, executor_id TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('held','closed')))",
    "CREATE UNIQUE INDEX storage_one_hold ON storage_windows(state) WHERE state='held'",
)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON field")
        result[key] = value
    return result


def matches(value, pattern):
    return isinstance(value, str) and re.fullmatch(pattern, value) is not None


def validate_snapshot(data):
    if (not isinstance(data, dict) or set(data) != {"protocol", "journal_id", "windows", "receipts"} or
            type(data["protocol"]) is not int or data["protocol"] != 1 or
            not matches(data["journal_id"], r"[0-9a-f]{32}")):
        raise ValueError("invalid guard snapshot")
    if any(not isinstance(data[key], list) or len(data[key]) > MAX_ROWS for key in ("windows", "receipts")):
        raise ValueError("invalid guard snapshot row count")
    previous, held = "", 0
    for row in data["windows"]:
        if (not isinstance(row, dict) or set(row) != {"id", "request_hash", "executor_id", "state"} or
                not matches(row["id"], r"[A-Za-z0-9_-]{16,128}") or row["id"] <= previous or
                not matches(row["request_hash"], r"[0-9a-f]{64}") or
                not matches(row["executor_id"], r"[0-9a-f]{64}") or row["state"] not in ("held", "closed")):
            raise ValueError("invalid guard snapshot window")
        previous = row["id"]
        held += row["state"] == "held"
    if held > 1:
        raise ValueError("multiple held windows")
    previous = ""
    for row in data["receipts"]:
        if (not isinstance(row, dict) or set(row) != {"id", "payload", "code"} or
                not matches(row["id"], r"[0-9a-f]{32}") or row["id"] <= previous or
                row["code"] not in ("not_started", "quiesced")):
            raise ValueError("invalid GPU receipt")
        permit = row["payload"]
        required = {"id", "instance_id", "token"}
        if (not isinstance(permit, dict) or set(permit) not in (required, required | {"work_id", "work_lease"}) or
                permit["id"] != row["id"] or not matches(permit["instance_id"], r"[0-9a-f]{32}") or
                not matches(permit["token"], r"[0-9a-f]{64}")):
            raise ValueError("invalid GPU receipt payload")
        if "work_id" in permit:
            for key in ("work_id", "work_lease"):
                if not isinstance(permit[key], str) or not 1 <= len(permit[key]) <= 96 or "\0" in permit[key]:
                    raise ValueError("invalid GPU work identity")
        previous = row["id"]
    if len(json.dumps(data, ensure_ascii=False, separators=(",", ":")).encode("utf-8")) > MAX_BYTES:
        raise ValueError("guard snapshot too large")
    return data


def snapshot(db, journal_id):
    db.execute("BEGIN")
    tables = {row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")}
    if tables != {"receipts", "storage_identity", "storage_windows"} or db.execute("SELECT 1 FROM sqlite_master WHERE type='trigger'").fetchone():
        raise ValueError("unsupported guard journal schema")
    for table, names in (("receipts", ("id", "payload", "code")),
                         ("storage_identity", ("singleton", "id")),
                         ("storage_windows", ("id", "request_hash", "executor_id", "state"))):
        if tuple(row[1] for row in db.execute("PRAGMA table_info(" + table + ")")) != names:
            raise ValueError("unsupported guard journal columns")
    if db.execute("SELECT singleton,id FROM storage_identity").fetchall() != [(1, journal_id)]:
        raise ValueError("guard journal identity changed")
    windows = [dict(zip(("id", "request_hash", "executor_id", "state"), row)) for row in
               db.execute("SELECT id,request_hash,executor_id,state FROM storage_windows ORDER BY id LIMIT ?", (MAX_ROWS + 1,))]
    receipts = []
    for identity, payload, code in db.execute("SELECT id,payload,code FROM receipts ORDER BY id LIMIT ?", (MAX_ROWS + 1,)):
        if not isinstance(payload, str) or len(payload.encode("utf-8")) > 8192:
            raise ValueError("GPU receipt too large")
        receipts.append({"id": identity, "payload": json.loads(payload, object_pairs_hook=unique_object), "code": code})
    return validate_snapshot({"protocol": 1, "journal_id": journal_id, "windows": windows, "receipts": receipts})


def restore(data, filename):
    data = validate_snapshot(data)
    path = Path(filename)
    if not path.is_absolute() or not path.parent.is_dir():
        raise ValueError("restore requires a new absolute destination")
    descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    os.close(descriptor)
    db = None
    try:
        db = sqlite3.connect(path)
        db.execute("PRAGMA synchronous=FULL")
        with db:
            for statement in SCHEMA:
                db.execute(statement)
            db.execute("INSERT INTO storage_identity VALUES(1,?)", (data["journal_id"],))
            db.executemany("INSERT INTO storage_windows VALUES(?,?,?,?)",
                           [(row["id"], row["request_hash"], row["executor_id"], row["state"]) for row in data["windows"]])
            db.executemany("INSERT INTO receipts VALUES(?,?,?)",
                           [(row["id"], json.dumps(row["payload"], sort_keys=True, separators=(",", ":")), row["code"]) for row in data["receipts"]])
        if db.execute("PRAGMA quick_check").fetchall() != [("ok",)] or snapshot(db, data["journal_id"]) != data:
            raise ValueError("restored guard journal differs")
        db.rollback()
        # A historical snapshot is evidence for reconciliation, not continuity.
        with db:
            db.execute("CREATE TABLE isolated_restore_blocked (reason TEXT NOT NULL)")
            db.execute("INSERT INTO isolated_restore_blocked VALUES ('isolated_restore_only')")
        db.close()
        db = None
        with path.open("r+b") as source:
            os.fsync(source.fileno())
    except BaseException:
        if db is not None:
            db.close()
        path.unlink(missing_ok=True)
        raise


def main():
    try:
        if len(sys.argv) != 2:
            raise ValueError("one destination is required")
        payload = sys.stdin.buffer.read(MAX_BYTES + 1)
        if len(payload) > MAX_BYTES:
            raise ValueError("snapshot too large")
        data = json.loads(payload, object_pairs_hook=unique_object)
        restore(data, sys.argv[1])
        print(json.dumps({"success": True, "journal_id": data["journal_id"],
                          "windows": len(data["windows"]), "receipts": len(data["receipts"])}))
    except Exception:
        # Payloads contain completion credentials. Never print them or a traceback.
        print(json.dumps({"success": False, "error": "guard_restore_failed"}))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
