#!/usr/bin/env python3
"""Repair native Codex fork attribution without changing original CAS payloads."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3

UUID_FILE = re.compile(r"([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$", re.I)


def encoded(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True)


def repair(path, device, ambiguous=None, ambiguous_claude=None):
    ambiguous = set(ambiguous or [])
    ambiguous_claude = set(ambiguous_claude or [])
    db = sqlite3.connect(path.resolve().as_uri() + "?mode=rw", uri=True)
    db.execute("PRAGMA busy_timeout=5000")
    fixes = []
    affected = set()
    for rid, old_session, raw_meta, raw_facets in db.execute("SELECT r.request_id,r.session_id,r.metadata_json,r.facets_json FROM records r JOIN record_facets f ON f.request_id=r.request_id WHERE r.source_format='native-history/v1' AND f.name='client' AND f.value IN ('codex','claude-code')"):
        meta, facets = json.loads(raw_meta), json.loads(raw_facets)
        if meta.get("native_device") != device:
            continue
        filename = Path(meta.get("native_path", "")).name
        client = meta.get("native_client") or facets.get("client", [""])[0]
        match = UUID_FILE.search(filename)
        old_native = meta.get("native_session_id", "")
        if client == "codex" and not match:
            continue
        native = match[1] if client == "codex" else old_native
        fork = client == "codex" and native != old_native
        mixed = native in ambiguous if client == "codex" else filename in ambiguous_claude
        if not fork and (not mixed or meta.get("native_scope_ambiguous")):
            continue
        session = "native:" + hashlib.sha256("\0".join([device, "codex", native, meta.get("native_branch", "")]).encode()).hexdigest() if fork else old_session
        if fork:
            meta["native_header_session_id"] = old_native
            meta["native_session_id"] = native
            meta["native_parent_id"] = meta.get("native_parent_id") or old_native
            facets["session.id"] = [session]
            facets["native.session_id"] = [native]
            facets["native.parent_id"] = [meta["native_parent_id"]]
        if mixed:
            meta["native_scope_ambiguous"] = True
            facets["project.id"] = ["native:" + device + ":ambiguous:" + native]
            facets["native.scope"] = ["ambiguous"]
        fixes.append((rid, old_session, session, encoded(meta), encoded(facets), raw_meta, raw_facets))
        affected.update([old_session, session])
    if not fixes:
        db.close()
        return {"repaired_segments": 0, "affected_sessions": 0}
    try:
        db.execute("BEGIN IMMEDIATE")
        for rid, old, new, meta, facets, raw_meta, raw_facets in fixes:
            changed = db.execute("UPDATE records SET session_id=?,metadata_json=?,facets_json=? WHERE request_id=? AND session_id=? AND metadata_json=? AND facets_json=?", (new, meta, facets, rid, old, raw_meta, raw_facets)).rowcount
            if changed != 1:
                raise RuntimeError("native source changed during repair; transaction rolled back")
            db.execute("UPDATE turn_records SET session_id=?,facets_json=? WHERE request_id=?", (new, facets, rid))
            values = json.loads(facets)
            for key in ("session.id", "native.session_id", "native.parent_id", "project.id", "native.scope"):
                if key not in values:
                    continue
                db.execute("DELETE FROM record_facets WHERE request_id=? AND name=?", (rid, key))
                db.execute("INSERT INTO record_facets(request_id,name,value) VALUES(?,?,?)", (rid, key, values[key][0]))
        db.execute("CREATE TEMP TABLE native_repair_sessions(session_id TEXT PRIMARY KEY)")
        db.executemany("INSERT INTO native_repair_sessions VALUES(?)", ((sid,) for sid in affected))
        for table in ("session_indexed_requests", "session_summaries", "session_facets", "turn_texts", "session_export_digests"):
            db.execute("DELETE FROM " + table + " WHERE session_id IN (SELECT session_id FROM native_repair_sessions)")
        db.execute("INSERT INTO session_indexed_requests SELECT request_id,session_id FROM records WHERE session_id IN (SELECT session_id FROM native_repair_sessions)")
        db.execute("INSERT INTO session_facets SELECT DISTINCT r.session_id,f.name,f.value FROM records r JOIN record_facets f USING(request_id) WHERE r.session_id IN (SELECT session_id FROM native_repair_sessions)")
        db.execute("""INSERT INTO session_summaries(session_id,requests,first_at,last_at,key_id,principal_id,model,project,summary,summary_at)
 SELECT r.session_id,COUNT(*),MIN(r.started_at),MAX(r.completed_at),
 COALESCE((SELECT key_id FROM records x WHERE x.session_id=r.session_id ORDER BY started_at LIMIT 1),''),
 COALESCE((SELECT principal_id FROM records x WHERE x.session_id=r.session_id AND COALESCE(principal_id,'')<>'' ORDER BY started_at LIMIT 1),''),
 COALESCE((SELECT requested_model FROM records x WHERE x.session_id=r.session_id AND COALESCE(requested_model,'')<>'' ORDER BY completed_at DESC LIMIT 1),''),
 COALESCE((SELECT value FROM session_facets x WHERE x.session_id=r.session_id AND name='project.name' LIMIT 1),''),
 COALESCE((SELECT summary FROM records x WHERE x.session_id=r.session_id AND COALESCE(summary,'')<>'' ORDER BY started_at LIMIT 1),''),
 COALESCE((SELECT started_at FROM records x WHERE x.session_id=r.session_id AND COALESCE(summary,'')<>'' ORDER BY started_at LIMIT 1),'')
 FROM records r JOIN native_repair_sessions a USING(session_id) GROUP BY r.session_id""")
        if db.execute("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='native_sync_files'").fetchone()[0]:
            for key, raw in db.execute("SELECT source_key,context_json FROM native_sync_files").fetchall():
                parts = key.split("\0")
                if len(parts) != 3 or parts[0] != device or parts[1] not in ("codex", "claude-code"):
                    continue
                match = UUID_FILE.search(parts[2]) if parts[1] == "codex" else None
                ctx = json.loads(raw)
                changed = False
                if match and match[1] != ctx.get("session_id"):
                    ctx["parent_id"] = ctx.get("parent_id") or ctx.get("session_id", "")
                    ctx["session_id"] = match[1]
                    changed = True
                mixed = bool(match and match[1] in ambiguous) if parts[1] == "codex" else parts[2] in ambiguous_claude
                if mixed and not ctx.get("scope_ambiguous"):
                    ctx["scope_ambiguous"] = True
                    changed = True
                if changed:
                    db.execute("UPDATE native_sync_files SET context_json=? WHERE source_key=?", (encoded(ctx), key))
        db.commit()
    except Exception:
        db.rollback()
        raise
    finally:
        db.close()
    return {"repaired_segments": len(fixes), "affected_sessions": len(affected)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", required=True, type=Path)
    parser.add_argument("--device", required=True)
    parser.add_argument("--report", required=True, type=Path)
    parser.add_argument("--scope-manifest", type=Path, help="audited mixed-workspace Codex session IDs")
    args = parser.parse_args()
    os.umask(0o077)
    with os.fdopen(os.open(str(args.archive) + ".lock", os.O_CREAT | os.O_RDWR, 0o600), "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        with os.fdopen(os.open(args.report, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "w") as report:
            manifest = json.loads(args.scope_manifest.read_text()) if args.scope_manifest else {}
            if manifest and manifest.get("device") != args.device:
                raise ValueError("scope manifest device mismatch")
            result = repair(args.archive, args.device, manifest.get("ambiguous_codex_sessions", []), manifest.get("ambiguous_claude_files", []))
            json.dump(result, report)
            report.flush()
            os.fsync(report.fileno())
        print(json.dumps(result))


if __name__ == "__main__":
    main()
