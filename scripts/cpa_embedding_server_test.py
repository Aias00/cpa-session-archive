"""HTTP/privacy/budget checks using a synthetic model, without optional packages."""

import http.client
import importlib.util
import json
from pathlib import Path
import tempfile
import threading
import unittest

spec = importlib.util.spec_from_file_location("cpa_embedding_server", Path(__file__).with_name("cpa-embedding-server.py"))
server_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(server_module)


class Model:
    def __init__(self):
        self.calls = []
        self.error = None
        self.started = None
        self.release = None

    def embed(self, purpose, texts):
        self.calls.append((purpose, texts))
        if self.started:
            self.started.set()
            self.release.wait(3)
        if self.error:
            raise self.error
        return [[1.0] + [0.0] * (server_module.DIMENSIONS - 1) for _ in texts]


class EmbeddingServerTest(unittest.TestCase):
    def setUp(self):
        self.model = Model()
        self.server = server_module.EmbeddingHTTPServer(("127.0.0.1", 0), b"t" * 48, self.model)
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        self.thread.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(3)

    def call(self, body=None, headers=None, path="/embed"):
        connection = http.client.HTTPConnection(*self.server.server_address, timeout=3)
        value = json.dumps(body).encode() if not isinstance(body, bytes) else body
        merged = {"Authorization": "Bearer " + "t" * 48, "Content-Type": "application/json"}
        merged.update(headers or {})
        connection.request("GET" if body is None else "POST", path, body=value if body is not None else None, headers=merged)
        response = connection.getresponse()
        result = response.status, json.loads(response.read())
        connection.close()
        return result

    def valid(self):
        return {"fingerprint": server_module.FINGERPRINT, "purpose": "query", "texts": ["中文 code/path.go"]}

    def test_authenticated_model_contract_and_required_fields(self):
        code, response = self.call(self.valid())
        self.assertEqual(code, 200)
        self.assertEqual(response["fingerprint"], server_module.FINGERPRINT)
        self.assertEqual(response["dimensions"], 384)
        self.assertEqual(len(response["vectors"][0]), 384)
        self.assertEqual(self.model.calls[0], ("query", ["中文 code/path.go"]))
        code, health = self.call(path="/health")
        self.assertEqual(code, 200)
        self.assertTrue(health["ready"])

    def test_auth_origin_and_malformed_inputs_do_not_reach_model(self):
        self.assertEqual(self.call(self.valid(), {"Authorization": "Bearer wrong"})[0], 401)
        self.assertEqual(self.call(self.valid(), {"Origin": "http://example.invalid"})[0], 403)
        for edit in (
            lambda value: value.update(fingerprint="wrong"),
            lambda value: value.update(fingerprint=True),
            lambda value: value.update(purpose="other"),
            lambda value: value.update(purpose=True),
            lambda value: value.update(texts=True),
            lambda value: value.update(texts=[]),
            lambda value: value.update(texts=["x"] * 9),
            lambda value: value.update(texts=["中"] * 257),
            lambda value: value.update(texts=["中" * 257]),
            lambda value: value.update(texts=["\ud800"]),
            lambda value: value.update(texts=[" "]),
            lambda value: value.update(texts=[{}]),
            lambda value: value.update(texts=[True]),
            lambda value: value.update(extra="private-body"),
            lambda value: value.pop("fingerprint"),
        ):
            value = self.valid()
            edit(value)
            code, error = self.call(value)
            self.assertEqual(code, 400)
            self.assertEqual(error, {"error": "invalid_request"})
        self.assertEqual(self.call(b'{"fingerprint":1,"fingerprint":2}')[0], 400)
        raw = json.dumps(self.valid())
        for prefix in (
            '"fingerprint":' + json.dumps(server_module.FINGERPRINT) + ',',
            '"purpose":"document",',
            '"texts":["another valid text"],',
        ):
            self.assertEqual(self.call(('{' + prefix + raw[1:]).encode())[0], 400)
        self.assertEqual(self.call(b"x" * (server_module.MAX_BODY + 1))[0], 413)
        self.assertEqual(self.model.calls, [])

    def test_failed_inference_and_token_overflow_never_return_empty_vectors(self):
        self.model.error = ValueError("private input and token must not be returned")
        self.assertEqual(self.call(self.valid()), (503, {"error": "inference_unavailable"}))
        self.model.error = server_module.TokenLimitError("private input")
        self.assertEqual(self.call(self.valid()), (422, {"error": "token_limit"}))

    def test_concurrent_inference_is_rejected_without_unbounded_queue(self):
        self.model.started = threading.Event()
        self.model.release = threading.Event()
        results = []
        worker = threading.Thread(target=lambda: results.append(self.call(self.valid())))
        worker.start()
        self.assertTrue(self.model.started.wait(2))
        try:
            self.assertEqual(self.call(self.valid()), (429, {"error": "busy"}))
        finally:
            self.model.release.set()
            worker.join(3)
        self.assertEqual(results[0][0], 200)
        self.assertEqual(len(self.model.calls), 1)


class LocalSafetyTest(unittest.TestCase):
    def test_token_files_and_model_hash_are_checked(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "token"
            path.write_bytes(b"t" * 48 + b"\n")
            path.chmod(0o600)
            self.assertEqual(server_module.private_token(path), b"t" * 48)
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                server_module.private_token(path)
            path.chmod(0o600)
            link = Path(directory) / "link"
            link.symlink_to(path)
            with self.assertRaises(ValueError):
                server_module.private_token(link)
            with self.assertRaises(ValueError):
                server_module.verify_file(path, server_module.MODEL_SHA256)

    def test_full_token_count_is_checked_before_inference(self):
        class Row:
            ids = list(range(513))

        class Tokenizer:
            def encode_batch(self, values, add_special_tokens):
                self.values = values
                return [Row()]

        model = server_module.LocalModel.__new__(server_module.LocalModel)
        model.np = None
        model.tokenizer = Tokenizer()
        with self.assertRaises(server_module.TokenLimitError):
            model.embed("document", ["full evidence"])
        self.assertEqual(model.tokenizer.values, ["passage: full evidence"])


if __name__ == "__main__":
    unittest.main()
