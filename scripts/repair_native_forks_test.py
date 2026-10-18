import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("repair_native_forks", Path(__file__).with_name("repair_native_forks.py"))
repair = importlib.util.module_from_spec(spec)
spec.loader.exec_module(repair)


class RepairTests(unittest.TestCase):
    def test_parent_and_fork_are_separate_and_originals_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "fixture.sqlite"
            with sqlite3.connect(path) as db:
                db.executescript("""CREATE TABLE records(request_id TEXT PRIMARY KEY,session_id TEXT,metadata_json TEXT,facets_json TEXT,source_format TEXT,started_at TEXT,completed_at TEXT,key_id TEXT,principal_id TEXT,requested_model TEXT,summary TEXT,original_ref TEXT);
CREATE TABLE record_facets(request_id TEXT,name TEXT,value TEXT);
CREATE TABLE turn_records(request_id TEXT,session_id TEXT,facets_json TEXT);
CREATE TABLE session_indexed_requests(request_id TEXT,session_id TEXT);
CREATE TABLE session_facets(session_id TEXT,name TEXT,value TEXT);
CREATE TABLE session_summaries(session_id TEXT,requests INTEGER,first_at TEXT,last_at TEXT,key_id TEXT,principal_id TEXT,model TEXT,project TEXT,summary TEXT,summary_at TEXT);
CREATE TABLE turn_texts(session_id TEXT);
CREATE TABLE session_export_digests(session_id TEXT);
CREATE TABLE native_sync_files(source_key TEXT,context_json TEXT);""")
                parent = "11111111-1111-1111-1111-111111111111"
                child = "22222222-2222-2222-2222-222222222222"
                for name, native in [("parent", parent), ("fork", child)]:
                    meta = {"native_device": "d", "native_session_id": parent, "native_path": "/sessions/rollout-" + native + ".jsonl"}
                    facets = {"session.id": ["old"], "client": ["codex"], "project.name": ["fixture"]}
                    db.execute("INSERT INTO records VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", (name, "old", json.dumps(meta), json.dumps(facets), "native-history/v1", "2026-10-18", "2026-10-18", "", "", "model", "user text", "immutable-" + name))
                    db.execute("INSERT INTO turn_records VALUES(?,?,?)", (name, "old", json.dumps(facets)))
                    for key, values in facets.items():
                        for value in values:
                            db.execute("INSERT INTO record_facets VALUES(?,?,?)", (name, key, value))
                    db.execute("INSERT INTO native_sync_files VALUES(?,?)", ("\0".join(["d", "codex", "rollout-" + native + ".jsonl"]), json.dumps({"session_id": parent})))
            result = repair.repair(path, "d")
            self.assertEqual(result["repaired_segments"], 1)
            with sqlite3.connect(path) as db:
                self.assertEqual(db.execute("SELECT COUNT(DISTINCT session_id) FROM records").fetchone()[0], 2)
                self.assertEqual(dict(db.execute("SELECT request_id,original_ref FROM records")), {"parent": "immutable-parent", "fork": "immutable-fork"})
                self.assertEqual(db.execute("SELECT SUM(requests) FROM session_summaries").fetchone()[0], 2)
                self.assertEqual(db.execute("SELECT COUNT(*) FROM session_indexed_requests").fetchone()[0], 2)
                self.assertEqual(db.execute("SELECT COUNT(*) FROM record_facets WHERE name='native.parent_id' AND value=?", (parent,)).fetchone()[0], 1)
            self.assertEqual(repair.repair(path, "d")["repaired_segments"], 0)


if __name__ == "__main__":
    unittest.main()
