# Local embedding provider

The optional CPU provider is isolated from the collector. It runs a pinned ONNX
model behind authenticated loopback HTTP; the Go `internal/embedding` client
never uses system proxies or follows redirects. A request failure is an error,
not an empty embedding result. This provider does not perform extraction or
review, and does not change the authority of raw archive evidence or reviewed Git.

The model is multilingual E5 Small, converted by Xenova:

| Property | Pinned value |
| --- | --- |
| Repository | `Xenova/multilingual-e5-small` |
| Revision | `761b726dd34fb83930e26aab4e9ac3899aa1fa78` |
| Model | `onnx/model_quantized.onnx` (118,308,185 bytes) |
| Model SHA-256 | `f80102d3f2a1229f387d3c81909990d8945513e347b0eab049f7de3c6f98c193` |
| Tokenizer SHA-256 | `0b44a9d7b51c3c62626640cda0e2c2f70fdacdc25bbbd68038369d14ebdf4c39` |
| Dimensions | 384 |
| Prefixes | `query: ` and `passage: `, including the space |
| Pooling | Attention-mask mean, then L2 normalization |
| Limits | 8 texts/batch; 256 query or 384 document Unicode codepoints; 512 tokens including prefix/special tokens |

The complete fingerprint returned by `/health` includes revision, model checksum,
runtime versions, prefixes, pooling and the document window policy (384 runes,
96-rune overlap). The caller creates windows; the provider rejects tokenizer
overflow and never silently truncates. A new model, tokenizer, prefix, window or
pooling policy requires a different fingerprint and rebuilt stored vectors.

This is a multilingual retrieval model, not a demonstrated code-specialized
model. Keep lexical retrieval for exact paths, identifiers and errors, and use
the frozen synthetic corpus plus real owner labels to evaluate hybrid ranking.

## Isolated setup

Use a compatible CPython 3.10 interpreter on Darwin arm64. The M4 has a managed
interpreter at the path below; the system Python 3.14 environment is separate.
The pinned requirements include transitive packages for a reproducible install.

```sh
/Users/aias/.local/share/uv/python/cpython-3.10.22-macos-aarch64-none/bin/python3.10 -m venv /Volumes/data/tools/cpa-embedding/venv
/Volumes/data/tools/cpa-embedding/venv/bin/python -m pip install -r scripts/cpa-embedding-requirements.txt
mkdir -p /Volumes/data/tools/cpa-embedding/models/e5-small-761b726d
curl --fail --location --output /Volumes/data/tools/cpa-embedding/models/e5-small-761b726d/model_quantized.onnx https://huggingface.co/Xenova/multilingual-e5-small/resolve/761b726dd34fb83930e26aab4e9ac3899aa1fa78/onnx/model_quantized.onnx
curl --fail --location --output /Volumes/data/tools/cpa-embedding/models/e5-small-761b726d/tokenizer.json https://huggingface.co/Xenova/multilingual-e5-small/resolve/761b726dd34fb83930e26aab4e9ac3899aa1fa78/tokenizer.json
```

An explicitly selected public mirror can supply those same pinned files if the
origin is unreachable; the server verifies both SHA-256 checksums before loading
either file. No model repository code, pickle checkpoint, model API or automatic
download is used at runtime. Review the base model's MIT terms and the selected
conversion/runtime packages' terms when redistributing artifacts.

Create a separate 0600 token in a private directory. Do not reuse archive-feed,
recall or management credentials; do not put its value in command arguments or
environment logs. Start the service using paths only:

```sh
/Volumes/data/tools/cpa-embedding/venv/bin/python scripts/cpa-embedding-server.py \
  --model-dir /Volumes/data/tools/cpa-embedding/models/e5-small-761b726d \
  --token-file /Volumes/data/tools/cpa-embedding/private/embedding.token \
  --listen 127.0.0.1 --port 8320 --threads 2
```

Both `GET /health` and `POST /embed` require the dedicated Bearer credential.
`/embed` accepts `{fingerprint,purpose,texts}` and returns
`{fingerprint,dimensions,vectors}`. It accepts one inference at a time, caps active
HTTP requests at two, and rejects excess work with 429. Bodies are capped at
64 KiB and sockets have a five-second I/O timeout. HTTP errors contain fixed
codes; request text and credentials are never logged. CPU inference already
running after a disconnected client may complete; callers must bound their
own wait and retry idempotently.

The Go client validates input limits, required response fields, vector count,
dimensions, fingerprint, finite values and nonzero norms. Unknown, duplicate or
case-aliased response control fields are rejected. It caps responses at
1 MiB and has a 15-second total timeout in addition to caller cancellation.

This is a local dependency setup, not a production rollout. CPU latency, RSS,
projection lag and retrieval benefit need measurement before enabling live
corpus indexing. Do not eagerly embed repeated request occurrences: cache by
normalized window content and full fingerprint while retaining each authorized
source occurrence.

## Verification

```sh
go test -race ./internal/embedding
python3 -m unittest discover -s scripts -p 'cpa_embedding_server_test.py'
```

The Python unit tests use a synthetic model and standard-library HTTP. Actual
model acceptance must separately run the pinned local model on synthetic text
and the frozen evaluation corpus; synthetic protocol tests do not prove semantic
quality or production performance.

An initial real-model smoke run on an Apple M4 with 16 GiB RAM and two inference
threads returned unit-length 384-dimensional vectors and identical output for a
repeated batch. Eight short synthetic documents took 17–20 ms; eight 364-codepoint
Chinese documents took 229 ms. Single short queries took approximately 3 ms.
Process RSS after these requests was approximately 463–531 MiB. A 384-codepoint
Arabic ligature input expanding to 1,540 tokens was rejected with 422 before
inference. These are individual local measurements, not a throughput or tail
latency benchmark. Cross-language synthetic paraphrases ranked above unrelated
documents in that smoke run; only the separate frozen evaluation can establish
retrieval quality or justify enabling semantic ranking.
