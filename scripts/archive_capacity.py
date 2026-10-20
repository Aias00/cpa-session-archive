#!/usr/bin/env python3
"""Report archive capacity and conservative backup protection; never delete files."""
import argparse
import json
import os
from pathlib import Path
import shutil
import stat
import sys


GIB = 1024 ** 3


def inventory(path):
    result = {"logical_bytes": 0, "allocated_bytes": 0, "files": 0,
              "symlinks_skipped": 0, "read_errors": 0}
    seen = set()

    def visit(current):
        try:
            info = current.lstat()
            if stat.S_ISLNK(info.st_mode):
                result["symlinks_skipped"] += 1
            elif stat.S_ISDIR(info.st_mode):
                for child in current.iterdir():
                    visit(child)
            elif stat.S_ISREG(info.st_mode):
                identity = (info.st_dev, info.st_ino)
                if identity not in seen:
                    seen.add(identity)
                    result["files"] += 1
                    result["logical_bytes"] += info.st_size
                    result["allocated_bytes"] += getattr(info, "st_blocks", 0) * 512
        except OSError:
            result["read_errors"] += 1

    if path.exists() or path.is_symlink():
        visit(path)
    return result


def read_json(path):
    if path.is_symlink() or path.stat().st_size > 1024 ** 2:
        raise ValueError("unsafe or oversized report")
    return json.loads(path.read_text())


def strings(value):
    if isinstance(value, str):
        yield value
    elif isinstance(value, dict):
        for item in value.values():
            yield from strings(item)
    elif isinstance(value, list):
        for item in value:
            yield from strings(item)


def recorded_pair(path):
    names = ("archive.sqlite", "memory.sqlite")
    for name in names:
        db = path / name
        wal = path / (name + "-wal")
        if db.is_symlink() or not db.is_file() or db.stat().st_size == 0:
            return False
        if wal.is_symlink() or wal.exists() and wal.stat().st_size:
            return False
    try:
        value = read_json(path / "verification.json")
        return (isinstance(value, dict) and
                (all(value.get(name) == "ok" for name in names) or
                 value.get("quick_check") == "ok" and
                 isinstance(value.get("databases"), list) and
                 all(name in value["databases"] for name in names)))
    except (OSError, ValueError, TypeError):
        return False


def report(root, free_bytes=None, reserve_bytes=10 * GIB):
    root = Path(root).absolute()
    if root.is_symlink() or not root.is_dir():
        raise ValueError("installation must be an existing directory, not a link")
    warnings = []
    references = []
    manifest = root / "install-manifest.json"
    if manifest.exists():
        try:
            references.extend((text, "manifest_reference") for text in strings(read_json(manifest)))
        except (OSError, ValueError, TypeError):
            warnings.append("manifest_unreadable")
    else:
        warnings.append("manifest_missing")
    for pointer in root.glob("*rollback-dir.txt"):
        try:
            if pointer.is_symlink() or pointer.stat().st_size > 8192:
                raise ValueError("unsafe rollback pointer")
            references.append((pointer.read_text().strip(), "rollback_pointer"))
        except (OSError, ValueError):
            warnings.append("rollback_pointer_unreadable")
    backups = []
    backup_root = root / "backups"
    if backup_root.is_symlink():
        warnings.append("backup_root_is_symlink")
    elif backup_root.exists():
        for path in sorted(backup_root.iterdir()):
            if path.is_symlink() or not path.is_dir():
                warnings.append("backup_entry_not_directory")
                continue
            size = inventory(path)
            protection = []
            for text, reason in references:
                if text == str(path) or text.startswith(str(path) + os.sep):
                    if reason not in protection:
                        protection.append(reason)
            if path.name.startswith("m1-to-m4"):
                protection.append("migration_baseline")
            verified = recorded_pair(path)
            if not verified:
                protection.append("integrity_not_recorded")
            if size["read_errors"] or size["symlinks_skipped"]:
                protection.append("incomplete_inventory")
            backups.append({"name": path.name, "mtime_ns": path.stat().st_mtime_ns,
                            "recorded_verified_pair": verified,
                            "protection": protection, **size})
    newest = sorted((item for item in backups if item["recorded_verified_pair"]),
                    key=lambda item: (item["mtime_ns"], item["name"]), reverse=True)[:2]
    for item in newest:
        item["protection"].append("newest_verified_pair")
    data = inventory(root / "data")
    if data["read_errors"] or data["symlinks_skipped"]:
        warnings.append("data_inventory_incomplete")
    for item in backups:
        if warnings:
            item["protection"].append("reference_inventory_incomplete")
    databases = {}
    for name in ("archive.sqlite", "archive.sqlite-wal", "memory.sqlite", "memory.sqlite-wal"):
        path = root / "data" / name
        if path.is_file() and not path.is_symlink():
            databases[name] = path.stat().st_size
    pair_bytes = sum(databases.values())
    if any(name not in databases for name in ("archive.sqlite", "memory.sqlite")):
        warnings.append("active_database_pair_missing")
    free = shutil.disk_usage(root).free if free_bytes is None else free_bytes
    headroom = pair_bytes + reserve_bytes
    status = "critical" if free < reserve_bytes else "warning" if free < headroom or warnings else "ok"
    return {"schema_version": 1, "status": status, "free_bytes": free,
            "reserve_bytes": reserve_bytes, "conservative_backup_pair_bytes": pair_bytes,
            "backup_headroom_bytes": headroom, "can_create_backup_pair": free >= headroom and not warnings,
            "data": data, "databases": databases, "backups": backups, "warnings": warnings,
            "cleanup_review_candidates": [item["name"] for item in backups if not item["protection"] and not warnings],
            "integrity_basis": "recorded metadata only; no fresh database validation",
            "reclaim_basis": "allocated bytes are estimates; APFS sharing/snapshots can reduce reclaimed space"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, type=Path)
    parser.add_argument("--reserve-gib", type=int, default=10)
    args = parser.parse_args()
    if args.reserve_gib < 1:
        parser.error("reserve-gib must be positive")
    try:
        result = report(args.root, reserve_bytes=args.reserve_gib * GIB)
        print(json.dumps(result, indent=2))
        return {"ok": 0, "warning": 1, "critical": 2}[result["status"]]
    except (OSError, ValueError):
        print("capacity inventory failed; inspect installation access", file=sys.stderr)
        return 3


if __name__ == "__main__":
    sys.exit(main())
