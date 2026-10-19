#!/usr/bin/env python3
"""Sync retained local client histories to the verified M4 archive."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import shlex
import sqlite3
import subprocess
import sys
import time


def run(args, **kwargs):
    return subprocess.run(args, check=True, text=True, capture_output=True, **kwargs).stdout


def private_json(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as stream:
        json.dump(value, stream, indent=2)
        stream.write("\n")
    os.replace(temporary, path)


def snapshot(source, target):
    """Use SQLite's online backup; never copy a live main file alone."""
    temporary = target.with_suffix(".incoming.sqlite")
    fd = os.open(temporary, os.O_CREAT | os.O_TRUNC | os.O_RDWR, 0o600)
    os.close(fd)
    with sqlite3.connect(source.resolve().as_uri() + "?mode=ro", uri=True) as src:
        with sqlite3.connect(temporary) as dst:
            src.backup(dst, pages=4096)
            if dst.execute("PRAGMA quick_check").fetchone()[0] != "ok":
                raise RuntimeError("staging snapshot integrity check failed")
    os.replace(temporary, target)


def load_config(path):
    if path.stat().st_mode & 0o077:
        raise ValueError("sync configuration must be private (0600)")
    cfg = json.loads(path.read_text())
    if cfg["ssh_host"] not in ("mac-mini-m4", "mac-mini"):
        raise ValueError("only the standing M4 SSH aliases are permitted")
    if not re.fullmatch(r"[A-Za-z0-9_-]+", cfg["device"]):
        raise ValueError("device must be a stable simple identifier")
    for key in ("codex_home", "claude_home"):
        if cfg.get(key) and not Path(cfg[key]).is_absolute():
            raise ValueError(key + " must be absolute")
    return cfg


def sync(config_path):
    cfg = load_config(config_path)
    root = Path(cfg["state_dir"])
    root.mkdir(parents=True, mode=0o700, exist_ok=True)
    (root / "reports").mkdir(mode=0o700, exist_ok=True)
    lock_path = root / "transfer.lock"
    fd = os.open(lock_path, os.O_CREAT | os.O_RDWR, 0o600)
    with os.fdopen(fd, "w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return {"skipped": "synchronization already running"}
        stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()) + "-" + str(time.time_ns())
        host = cfg["ssh_host"]

        def remote(args):
            return run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", host, shlex.join(args)])

        report = root / "reports" / (stamp + "-local.json")
        collect = [cfg["binary"], "--device", cfg["device"], "--archive", str(root / "staging.sqlite"), "--report", str(report)]
        for key, flag in (("codex_home", "--codex-home"), ("claude_home", "--claude-home")):
            if cfg.get(key):
                collect.extend([flag, cfg[key]])
        summary = json.loads(run(collect))
        # A previous transfer may have failed after local commit. Always compare
        # the durable local watermark with the last fully acknowledged transfer.
        with sqlite3.connect((root / "staging.sqlite").resolve().as_uri() + "?mode=ro", uri=True) as db:
            watermark = db.execute("SELECT sequence FROM archive_ingest_clock WHERE id=1").fetchone()[0]
        state_path = root / "transfer-state.json"
        state = json.loads(state_path.read_text()) if state_path.exists() else {}
        if state.get("watermark") == watermark:
            result = {"archive_committed": True, "transfer_needed": False, "local": summary}
            private_json(root / "last-success.json", {"completed_at": stamp, **result})
            return result

        hardware = remote(["/usr/sbin/sysctl", "-n", "machdep.cpu.brand_string", "hw.model"])
        if hardware.strip().splitlines() != ["Apple M4", "Mac16,10"]:
            raise RuntimeError("M4 hardware verification failed; destination untouched")
        if "watermark" not in state:
            transfer = root / "transfer.sqlite"
            snapshot(root / "staging.sqlite", transfer)
        else:
            transfer = root / "reports" / (stamp + "-delta.sqlite")
            run([cfg["binary"], "--device", cfg["device"], "--archive", str(root / "staging.sqlite"), "--export", str(transfer), "--after", str(state["watermark"]), "--through", str(watermark)])
            with sqlite3.connect(transfer.resolve().as_uri() + "?mode=ro", uri=True) as db:
                if db.execute("PRAGMA quick_check").fetchone()[0] != "ok":
                    raise RuntimeError("delta archive integrity check failed")
        remote_dir = "/Users/aias/Work/data/cpa-native-history/" + cfg["device"]
        remote(["mkdir", "-p", remote_dir])
        remote(["chmod", "700", remote_dir])
        # Delta transfer is safe here: only this locked job writes the private
        # snapshot, and the remote importer starts after rsync succeeds.
        remote(["touch", remote_dir + "/source.sqlite"])
        remote(["chmod", "600", remote_dir + "/source.sqlite"])
        run(["rsync", "-t", "--inplace", "--no-whole-file", str(transfer), host + ":" + remote_dir + "/source.sqlite"])
        archive_root = "/Users/aias/Library/Application Support/CLIProxyAPI/session-archive"
        imported = json.loads(remote([archive_root + "/bin/cpa-session-import", "--source", remote_dir + "/source.sqlite", "--target", archive_root + "/data/archive.sqlite", "--report", remote_dir + "/" + stamp + "-import.json", "--batch", "8"]))
        if imported["remaining"] != 0 or imported["conflicts_preserved_on_target"] != 0:
            raise RuntimeError("import incomplete or conflicting; transfer checkpoint not advanced")
        status_code = """
import json, pathlib, urllib.request
root=pathlib.Path('/Users/aias/Library/Application Support/CLIProxyAPI/session-archive')
token=(root/'private/memory-read.token').read_text().strip()
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
assert opener.open('http://127.0.0.1:8318/readyz').status==200
request=urllib.request.Request('http://127.0.0.1:8319/v1/status',headers={'Authorization':'Bearer '+token})
status=json.load(opener.open(request))
print(json.dumps({'lag':status['lag'],'failures':status['failures'],'sources':status['sources']}))
"""
        recall = json.loads(remote(["python3", "-c", status_code]))
        private_json(state_path, {"watermark": watermark, "completed_at": stamp})
        if transfer.name.endswith("-delta.sqlite"):
            transfer.unlink()
        result = {"archive_committed": True, "transfer_needed": True, "local": summary, "import": imported, "recall": recall}
        private_json(root / "last-success.json", {"completed_at": stamp, **result})
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True, type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        result = sync(args.config)
        print(json.dumps(result))
    except (OSError, ValueError, KeyError, RuntimeError, sqlite3.Error, subprocess.CalledProcessError) as exc:
        # Do not echo subprocess arguments, stderr or history data to launchd logs.
        print("native history transfer failed: " + type(exc).__name__, file=sys.stderr)
        try:
            cfg = load_config(args.config)
            private_json(Path(cfg["state_dir"]) / "last-failure.json", {"failed_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "error_class": type(exc).__name__})
        except (OSError, ValueError, KeyError):
            pass
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
