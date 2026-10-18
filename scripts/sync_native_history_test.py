import importlib.util
from pathlib import Path
import sqlite3
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("sync_native_history", Path(__file__).with_name("sync_native_history.py"))
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class SyncTests(unittest.TestCase):
    def test_online_snapshot_includes_wal(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            source, target = root / "live.sqlite", root / "snapshot.sqlite"
            with sqlite3.connect(source) as db:
                db.execute("PRAGMA journal_mode=WAL")
                db.execute("CREATE TABLE events(value TEXT)")
                db.execute("INSERT INTO events VALUES('retained')")
                db.commit()
                sync.snapshot(source, target)
                with sqlite3.connect(target) as result:
                    self.assertEqual(result.execute("SELECT value FROM events").fetchone(), ("retained",))
            self.assertEqual(target.stat().st_mode & 0o777, 0o600)

    def test_private_config_and_m4_alias(self):
        with tempfile.TemporaryDirectory() as name:
            path = Path(name) / "config.json"
            sync.private_json(path, {"ssh_host": "mac-mini-m1", "device": "device"})
            with self.assertRaises(ValueError):
                sync.load_config(path)
            sync.private_json(path, {"ssh_host": "mac-mini-m4", "device": "device"})
            self.assertEqual(sync.load_config(path)["device"], "device")
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                sync.load_config(path)


if __name__ == "__main__":
    unittest.main()
