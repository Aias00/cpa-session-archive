import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("archive_capacity", Path(__file__).with_name("archive_capacity.py"))
capacity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capacity)


class CapacityTests(unittest.TestCase):
    def pair(self, root, name, mtime):
        path = root / "backups" / name
        path.mkdir(parents=True)
        if not (root / "install-manifest.json").exists():
            (root / "install-manifest.json").write_text("{}")
        for db in ("archive.sqlite", "memory.sqlite"):
            (path / db).write_bytes(b"fixture")
        (path / "verification.json").write_text(json.dumps({"archive.sqlite": "ok", "memory.sqlite": "ok"}))
        os.utime(path, (mtime, mtime))
        return path

    def test_protects_two_newest_verified_pairs_and_manifest_references(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, stamp in [("old", 10), ("middle", 20), ("new", 30), ("pinned", 5)]:
                self.pair(root, name, stamp)
            (root / "install-manifest.json").write_text(json.dumps({"nested": {"rollback": str(root / "backups/pinned/collector")}}))
            result = capacity.report(root)
            backups = {entry["name"]: entry for entry in result["backups"]}
            self.assertEqual(backups["old"]["protection"], [])
            self.assertIn("newest_verified_pair", backups["new"]["protection"])
            self.assertIn("newest_verified_pair", backups["middle"]["protection"])
            self.assertIn("manifest_reference", backups["pinned"]["protection"])

    def test_pointer_and_migration_baseline_are_protected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.pair(root, "pointed", 10)
            self.pair(root, "m1-to-m4-target", 20)
            (root / "current-rollback-dir.txt").write_text(str(root / "backups/pointed"))
            result = capacity.report(root)
            entries = {entry["name"]: entry for entry in result["backups"]}
            self.assertIn("rollback_pointer", entries["pointed"]["protection"])
            self.assertIn("migration_baseline", entries["m1-to-m4-target"]["protection"])

    def test_unknown_or_partial_verification_never_counts_as_complete_pair(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pair = self.pair(root, "unverified", 10)
            (pair / "verification.json").write_text('{"quick_check":"ok"}')
            result = capacity.report(root)
            self.assertFalse(result["backups"][0]["recorded_verified_pair"])
            self.assertIn("integrity_not_recorded", result["backups"][0]["protection"])
            (pair / "verification.json").write_text('{"quick_check":"ok","databases":["archive.sqlite","memory.sqlite"]}')
            self.assertTrue(capacity.report(root)["backups"][0]["recorded_verified_pair"])

    def test_never_follows_links_or_counts_hardlinks_twice(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "installation"
            data = root / "data"
            data.mkdir(parents=True)
            outside = Path(directory) / "private"
            outside.mkdir()
            (outside / "unrelated").write_bytes(b"x" * 1000)
            (data / "archive.sqlite").write_bytes(b"abc")
            os.link(data / "archive.sqlite", data / "hardlink")
            (data / "outside").symlink_to(outside, target_is_directory=True)
            result = capacity.report(root)
            self.assertEqual(result["data"]["logical_bytes"], 3)
            self.assertEqual(result["data"]["symlinks_skipped"], 1)
            self.assertEqual((outside / "unrelated").read_bytes(), b"x" * 1000)

    def test_capacity_accounts_for_wal_and_backup_reserve(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "data").mkdir()
            (root / "install-manifest.json").write_text("{}")
            for name in ["archive.sqlite", "archive.sqlite-wal", "memory.sqlite"]:
                (root / "data" / name).write_bytes(b"x" * 10)
            result = capacity.report(root, free_bytes=29, reserve_bytes=10)
            self.assertEqual(result["conservative_backup_pair_bytes"], 30)
            self.assertEqual(result["backup_headroom_bytes"], 40)
            self.assertEqual(result["status"], "warning")
            self.assertEqual(capacity.report(root, free_bytes=9, reserve_bytes=10)["status"], "critical")
            self.assertEqual(capacity.report(root, free_bytes=40, reserve_bytes=10)["status"], "ok")

    def test_missing_manifest_or_database_cannot_approve_backup_preflight(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = capacity.report(root, free_bytes=10 ** 12)
            self.assertFalse(result["can_create_backup_pair"])
            self.assertIn("manifest_missing", result["warnings"])
            self.assertIn("active_database_pair_missing", result["warnings"])

    def test_linked_backup_root_is_never_scanned(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "installation"
            root.mkdir()
            outside = Path(directory) / "outside"
            outside.mkdir()
            (root / "backups").symlink_to(outside, target_is_directory=True)
            result = capacity.report(root)
            self.assertEqual(result["backups"], [])
            self.assertIn("backup_root_is_symlink", result["warnings"])

    def test_loose_backup_files_are_inventoried_and_retained(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.pair(root, "complete", 10)
            path = root / "backups" / "old-collector"
            path.write_bytes(b"retained executable")
            result = capacity.report(root)
            loose = result["loose_backup_files"]
            self.assertEqual(len(loose), 1)
            self.assertEqual(loose[0]["name"], "old-collector")
            self.assertEqual(loose[0]["logical_bytes"], path.stat().st_size)
            self.assertIn("unclassified_backup_file", loose[0]["protection"])
            self.assertNotIn("backup_entry_not_directory", result["warnings"])

    def test_report_does_not_change_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.pair(root, "retained", 10)
            before = {str(p.relative_to(root)): (p.stat().st_size, p.stat().st_mtime_ns) for p in root.rglob("*")}
            capacity.report(root)
            after = {str(p.relative_to(root)): (p.stat().st_size, p.stat().st_mtime_ns) for p in root.rglob("*")}
            self.assertEqual(before, after)


if __name__ == "__main__":
    unittest.main()
