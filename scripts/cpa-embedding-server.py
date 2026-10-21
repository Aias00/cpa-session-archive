#!/usr/bin/env python3
"""Bounded local CPU embeddings; never fetch models or execute remote code."""

import argparse
import hashlib
import hmac
import importlib.metadata
import json
import math
import os
from pathlib import Path
import signal
import stat
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import threading

MODEL_REVISION = "761b726dd34fb83930e26aab4e9ac3899aa1fa78"
MODEL_SHA256 = "f80102d3f2a1229f387d3c81909990d8945513e347b0eab049f7de3c6f98c193"
TOKENIZER_SHA256 = "0b44a9d7b51c3c62626640cda0e2c2f70fdacdc25bbbd68038369d14ebdf4c39"
FINGERPRINT = (
    f"e5-small-onnx-v1:{MODEL_REVISION}:{MODEL_SHA256}:"
    "ort1.23.2:tok0.23.3:mean-mask:l2:query-passage:tok512-reject:r384-o96"
)
DIMENSIONS = 384
MAX_BATCH = 8
MAX_BODY = 64 << 10
MAX_TOKENS = 512


def private_token(path):
    info = os.lstat(path)
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) & 0o077:
        raise ValueError("token file must be a private regular file (0600)")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) & 0o077:
            raise ValueError("token file must be a private regular file (0600)")
        raw = stream.read(4097)
    if len(raw) > 4096:
        raise ValueError("invalid token file")
    token = raw.strip()
    if len(token) < 32 or any(value <= 32 or value >= 127 for value in token):
        raise ValueError("invalid token file")
    return token


def verify_file(path, expected):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(8 << 20), b""):
            digest.update(block)
    if not hmac.compare_digest(digest.hexdigest(), expected):
        raise ValueError("model or tokenizer checksum mismatch")


class TokenLimitError(ValueError):
    pass


class LocalModel:
    def __init__(self, directory, threads):
        for name, version in (("onnxruntime", "1.23.2"), ("tokenizers", "0.23.3"), ("numpy", "1.26.4")):
            if importlib.metadata.version(name) != version:
                raise ValueError("embedding runtime versions differ from the pinned fingerprint")
        model_path = directory / "model_quantized.onnx"
        tokenizer_path = directory / "tokenizer.json"
        verify_file(model_path, MODEL_SHA256)
        verify_file(tokenizer_path, TOKENIZER_SHA256)
        import numpy as np
        import onnxruntime as ort
        from tokenizers import Tokenizer

        self.np = np
        self.tokenizer = Tokenizer.from_file(str(tokenizer_path))
        # The tokenizer file may carry a truncation/padding policy. Count the
        # complete input first and reject overflow instead of hiding evidence.
        self.tokenizer.no_truncation()
        self.tokenizer.no_padding()
        self.pad = self.tokenizer.token_to_id("<pad>")
        if self.pad is None:
            raise ValueError("tokenizer padding identity unavailable")
        options = ort.SessionOptions()
        options.intra_op_num_threads = threads
        options.inter_op_num_threads = 1
        options.execution_mode = ort.ExecutionMode.ORT_SEQUENTIAL
        options.log_severity_level = 3
        self.session = ort.InferenceSession(str(model_path), sess_options=options, providers=["CPUExecutionProvider"])
        self.inputs = {item.name for item in self.session.get_inputs()}
        if not {"input_ids", "attention_mask"} <= self.inputs:
            raise ValueError("unexpected embedding model inputs")

    def embed(self, purpose, texts):
        np = self.np
        prefix = "query: " if purpose == "query" else "passage: "
        rows = self.tokenizer.encode_batch([prefix + text for text in texts], add_special_tokens=True)
        if any(len(row.ids) > MAX_TOKENS for row in rows):
            raise TokenLimitError("token_limit")
        width = max(len(row.ids) for row in rows)
        ids = np.full((len(rows), width), self.pad, dtype=np.int64)
        mask = np.zeros((len(rows), width), dtype=np.int64)
        types = np.zeros((len(rows), width), dtype=np.int64)
        for index, row in enumerate(rows):
            size = len(row.ids)
            ids[index, :size] = row.ids
            mask[index, :size] = row.attention_mask
            types[index, :size] = row.type_ids
        values = {"input_ids": ids, "attention_mask": mask}
        if "token_type_ids" in self.inputs:
            values["token_type_ids"] = types
        hidden = self.session.run(None, values)[0]
        if hidden.shape != (len(rows), width, DIMENSIONS):
            raise ValueError("unexpected embedding model output")
        weights = mask.astype(np.float32)
        vectors = (hidden * weights[..., None]).sum(axis=1) / weights.sum(axis=1, keepdims=True)
        norms = np.linalg.norm(vectors, axis=1, keepdims=True)
        if not np.isfinite(vectors).all() or not np.isfinite(norms).all() or (norms <= 0).any():
            raise ValueError("invalid embedding model output")
        return (vectors / norms).astype(np.float32).tolist()


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON field")
        result[key] = value
    return result


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def log_message(self, *_args):
        # BaseHTTPRequestHandler normally logs URL and request failures.
        pass

    def reply(self, status, value):
        data = json.dumps(value, allow_nan=False, separators=(",", ":")).encode("utf-8")
        try:
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.send_header("Cache-Control", "no-store")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(data)
        except (OSError, TimeoutError):
            pass

    def authenticate(self):
        if self.headers.get_all("Origin") or self.headers.get("Sec-Fetch-Site", "") not in ("", "none"):
            self.reply(403, {"error": "forbidden"})
            return False
        credentials = self.headers.get_all("Authorization", [])
        if len(credentials) != 1 or not hmac.compare_digest(credentials[0].encode("utf-8"), b"Bearer " + self.server.token):
            self.reply(401, {"error": "unauthorized"})
            return False
        return True

    def do_GET(self):
        if not self.authenticate():
            return
        if self.path != "/health":
            self.reply(404, {"error": "not_found"})
            return
        self.reply(200, {"fingerprint": FINGERPRINT, "dimensions": DIMENSIONS, "ready": True})

    def do_POST(self):
        if not self.authenticate():
            return
        if self.path != "/embed":
            self.reply(404, {"error": "not_found"})
            return
        lengths = self.headers.get_all("Content-Length", [])
        if len(lengths) != 1 or not lengths[0].isascii() or not lengths[0].isdigit() or self.headers.get_all("Transfer-Encoding"):
            self.reply(400, {"error": "invalid_request"})
            return
        length = int(lengths[0])
        if length > MAX_BODY:
            self.reply(413, {"error": "request_too_large"})
            return
        if len(self.headers.get_all("Content-Type", [])) != 1 or self.headers.get_content_type() != "application/json" or self.headers.get_all("Content-Encoding"):
            self.reply(400, {"error": "invalid_request"})
            return
        try:
            raw = self.rfile.read(length)
            value = json.loads(raw.decode("utf-8"), object_pairs_hook=unique_object)
            if len(raw) != length or not isinstance(value, dict) or set(value) != {"fingerprint", "purpose", "texts"}:
                raise ValueError("invalid request")
            if value["fingerprint"] != FINGERPRINT or value["purpose"] not in ("query", "document"):
                raise ValueError("invalid model identity or purpose")
            texts = value["texts"]
            limit = 256 if value["purpose"] == "query" else 384
            if not isinstance(texts, list) or not 1 <= len(texts) <= MAX_BATCH:
                raise ValueError("invalid batch")
            for text in texts:
                if not isinstance(text, str) or not text.strip() or len(text) > limit:
                    raise ValueError("invalid text")
                text.encode("utf-8")
        except (ValueError, UnicodeError, TypeError, OSError, TimeoutError):
            self.reply(400, {"error": "invalid_request"})
            return
        if not self.server.inference.acquire(blocking=False):
            self.reply(429, {"error": "busy"})
            return
        try:
            vectors = self.server.model.embed(value["purpose"], texts)
            if len(vectors) != len(texts) or any(len(row) != DIMENSIONS or not all(math.isfinite(item) for item in row) or not any(row) for row in vectors):
                raise ValueError("invalid embedding output")
            self.reply(200, {"fingerprint": FINGERPRINT, "dimensions": DIMENSIONS, "vectors": vectors})
        except TokenLimitError:
            self.reply(422, {"error": "token_limit"})
        except Exception:
            self.reply(503, {"error": "inference_unavailable"})
        finally:
            self.server.inference.release()


class EmbeddingHTTPServer(ThreadingHTTPServer):
    request_queue_size = 2
    daemon_threads = True

    def __init__(self, address, token, model):
        self.token = token
        self.model = model
        self.inference = threading.BoundedSemaphore(1)
        self.requests = threading.BoundedSemaphore(2)
        super().__init__(address, Handler)

    def get_request(self):
        request, address = super().get_request()
        request.settimeout(5)
        return request, address

    def process_request(self, request, address):
        if not self.requests.acquire(blocking=False):
            body = b'{"error":"busy"}'
            try:
                request.sendall(b"HTTP/1.0 429 Too Many Requests\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: " + str(len(body)).encode("ascii") + b"\r\n\r\n" + body)
            except OSError:
                pass
            self.shutdown_request(request)
            return
        try:
            super().process_request(request, address)
        except BaseException:
            self.requests.release()
            raise

    def process_request_thread(self, request, address):
        try:
            super().process_request_thread(request, address)
        finally:
            self.requests.release()

    def handle_error(self, request, address):
        # Unexpected handler failures must not print private request context.
        self.shutdown_request(request)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model-dir", type=Path, required=True)
    parser.add_argument("--token-file", type=Path, required=True)
    parser.add_argument("--listen", default="127.0.0.1", choices=("127.0.0.1",))
    parser.add_argument("--port", type=int, default=8320)
    parser.add_argument("--threads", type=int, default=2, choices=range(1, 5))
    args = parser.parse_args()
    if not 1 <= args.port <= 65535:
        parser.error("port must be between 1 and 65535")
    token = private_token(args.token_file)
    model = LocalModel(args.model_dir, args.threads)
    server = EmbeddingHTTPServer((args.listen, args.port), token, model)
    stop = threading.Event()

    def terminate(_number, _frame):
        if not stop.is_set():
            stop.set()
            threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    print(f"local embedding listening on {args.listen}:{args.port}; dimensions={DIMENSIONS}; cpu_threads={args.threads}", flush=True)
    try:
        server.serve_forever(poll_interval=0.2)
    finally:
        server.server_close()


if __name__ == "__main__":
    try:
        main()
    except Exception:
        raise SystemExit("local embedding server startup failed; check private credentials, pinned dependencies and model files") from None
