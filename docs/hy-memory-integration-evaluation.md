# Hy-Memory and MemHub integration evaluation

Date: 2026-10-17

Status: evaluated proposal. No Hy-Memory or MemHub installation, adapter, data
migration or production configuration change has been implemented.

## Goal and evidence

Evaluate whether Tencent-Hunyuan/Hy-Memory can turn this fork's multi-client
conversation archive into searchable project facts and reusable experience.
Compare it with the [ai-memory evaluation](ai-memory-integration-evaluation.md),
while keeping the original archive available for inspection and reprocessing.

- This fork's storage boundary: commit `4e01221d31c44767450033d213578dd1756c3558`,
  based on Session Archive v0.8.12.
- Hy-Memory source reviewed at
  [`f79770cdb098534d2444178347ad5adc7c473135`](https://github.com/Tencent-Hunyuan/Hy-Memory/tree/f79770cdb098534d2444178347ad5adc7c473135).
- Reviewed SDK version: `hy-memory==2.0.2`; MemHub source version: `1.0.2`.
- GitHub Release v1.0.2 lists macOS arm64 App/runtime artifacts. Their presence
  does not establish installation, performance or integration on our Mac mini.
- Evidence consists of selected production source, dependency declarations and
  documentation. No build, runtime, retrieval benchmark or model call was run.

## Decision

Hy-Memory is a candidate memory-processing engine, not a replacement for
`internal/storage.Backend`. MemHub is a product and integration reference built
around that engine. Preserve the existing raw archive and evaluate a background
adapter through a separate service boundary.

Do not write to Hy-Memory or MemHub databases directly, point them at our archive
as their business database, or claim that adding a vector backend implements the
complete Session Archive storage contract.

## Engine versus product

| Component | Responsibility | Important boundary |
| --- | --- | --- |
| Hy Memory Python SDK | Extraction, reconciliation, retrieval and coding-memory processing | Direct `add/search` calls do not include MemHub's Evidence, job or Governance layer |
| SDK HTTP wrapper | Lightweight local add/search/CRUD API | Ordinary HTTP, not the complete MemHub MCP/product runtime |
| MemHub | Source adapters, Evidence, durable processing jobs, governance, UI and host integrations | Python Core owns the business data; clients and source readers do not write it directly |

MemHub's reviewed configuration uses Chroma vectors, Kuzu graph data and SQLite
cache/history, with a separate SQLite business ledger. The SDK can choose other
vector providers, but that does not mean the complete MemHub product supports
arbitrary interchangeable business databases.

## Comparison with ai-memory

| Area | ai-memory reviewed design | Hy-Memory / MemHub reviewed design |
| --- | --- | --- |
| Knowledge representation | Human-editable Markdown wiki with Git versions | Structured facts, experiences, relations and evolution chains; MemHub adds governed views |
| Basic processing | Rule-based summaries and lexical retrieval without a cloud LLM; best-effort local embeddings | `lite` is embedding-only; `pro` adds LLM extraction/reconciliation; `ultra` adds explicit System 2 evolution |
| Storage | Markdown and SQLite indexes/operational state | Chroma, Qdrant or FAISS vectors; optional graph stores; SQLite working/coding metadata |
| Coding-specific value | Searchable decisions, knowledge pages and cross-agent handoffs | Session facts, reusable engineering experiences and deterministic code-revision context |
| Chinese lexical handling | Reviewed FTS tokenizer needs Chinese evaluation | Coding lexical reranking uses jieba, with CJK bigram fallback; this is not a quality benchmark |
| Cross-client access | MCP plus harness-specific hooks | MemHub Host Bridge, MCP, hooks and source adapters; not supplied by the SDK alone |
| Operational shape | Primarily a Rust server binary | Python SDK; full MemHub adds Core, host/runtime control, Web and Desktop components |
| Preferred role here | Candidate knowledge/recall service | Candidate extraction engine, or a separate product trial and architecture reference |

Neither candidate has been selected as our production memory system. Avoid
running both as competing automatic long-term memory authorities before a
bounded comparison establishes a reason to do so.

## Designs worth adopting

### Separate facts from reusable experience

Working/Coding memory distinguishes a session fact (task, approach, outcome and
source details) from zero or more experiences (reusable solution, search keys,
applicable environment and confidence). This is a useful alternative to one
unstructured summary for an entire session.

Preserve source request, message, tool-call and event-time references when
mapping these objects into our own product. A generated fact remains a claim
that can be corrected, not a replacement for the source evidence.

### Evidence before model work

MemHub describes the normal path as:

```text
authorized source -> Evidence -> complete turn -> durable memory_write_jobs
  -> model permission check -> Hy Memory bridge -> write verification
  -> Governance -> retrieval / UI / authorized host
```

Source evidence survives a failed model job. The production queue, source
ordering, correction/deletion state and uncertain outcomes are responsibilities
of the surrounding product, not guaranteed by an SDK success response.

The reviewed Governance implementation includes revision checks, source
existence checks, mutation retry identities, correction/undo, tombstones and
pending index-deletion state. Broader runtime composition was inspected through
architecture documentation and selected files, not executed end to end.

### Keep code evolution beside semantic summaries

Coding memory can attach local Git diffs or recognizable deterministic tool
edits to retrieved facts. This reduces dependence on an LLM summary retaining
every reverted implementation.

The chain is best-effort: unsupported edits may be skipped, and it does not
constitute a complete version ledger. A Mac mini cannot read a MacBook working
tree merely because CPA saw its requests. Client-side Git evidence or sufficient
tool traces must be supplied explicitly.

## Proposed integration

```text
CPA plugin -> Session Archive committed records
                         |
                durable adapter jobs
                         |
            normalize roles / remove replayed history
            map project / sanitize / retain source IDs
                         |
             separate Hy Memory worker/service
                         |
            verified facts + experiences + provenance
                         |
                governed recall API / MCP
```

Keep extraction outside CPA request latency. Jobs should be derived from
committed archive records using an outbox or a verified resumable event reader.
That mechanism is proposed, not implemented by the existing storage refactor.

Two experiment paths are valid:

1. Trial MemHub separately, using its source grants and public integration
   boundaries, to assess whether the existing product already meets our needs.
2. Retain our own product and call the SDK through a small isolated Python
   service, starting with explicit `scene` and `pro` mode. Our service must then
   own authorization, source mapping, durable jobs, verification, correction,
   deletion and recall transport. Do not assume the SDK provides those MemHub
   features.

If integrating with MemHub, use a proper authorized source/import adapter and
its Evidence-to-Governance path. Confirm the supported external intake contract
before implementation; no ready-made CPA Session Archive adapter was verified.

## Configuration and capability caveats

- `lite` performs vector storage/retrieval without LLM extraction. It still needs
  an embedding implementation and does not produce the pro-mode facts/experience
  pipeline. Normal semantic operation is not a zero-model feature.
- `pro` adds model-based extraction and reconciliation. `ultra` enables graph
  functionality and explicit `evolve()` processing; do not treat it as an
  automatically running background cognition loop.
- Explicit Working/Coding writes need tool traces; the reviewed implementation
  falls back to Chat when the required trace is missing. Preserve tool calls,
  results, IDs, names and authorship during normalization.
- With tools and no explicit scene, automatic classification can call an LLM.
  Explicit scene selection avoids that classification work where appropriate.
- The published 30B model is not mandatory. The SDK accepts configured
  OpenAI-compatible LLM and embedding endpoints, including suitable local ones.
  CPA may serve extraction calls after compatibility testing; its reviewed routes
  do not expose a general `/v1/embeddings` endpoint, so embedding needs a separate
  confirmed provider.
- Chroma, Qdrant and FAISS support is implemented by a vector-store factory.
  These are memory indexes, not complete replacements for our raw archive,
  export and stable-snapshot backend. Kuzu/Neo4j and MySQL cache options likewise
  do not imply a general PostgreSQL or object-storage adapter.
- Collection names include embedding dimensions, but equal dimensions do not
  make different embedding models interchangeable. Track provider, model,
  version, normalization and dimensions; rebuild incompatible vectors.
- Coding lexical processing can help Chinese terms and code identifiers, but it
  reranks dense candidates. It cannot recover a relevant fact that never entered
  that candidate set.
- Dependency and backup scope is larger than one SQLite file. Evaluate native
  Python dependencies on macOS arm64 and coordinate evidence, metadata, vectors
  and graph restoration before production adoption.
- MemHub separates source access, synchronization, model-content processing and
  host recall. Local-first storage does not mean no network egress: configured
  remote LLM/embedding providers can receive authorized content.
- Root LICENSE declares Apache-2.0 without added restrictions. Retain applicable
  copyright, change notices and third-party attribution when copying code.

## Experiment and acceptance criteria

1. Pin versions and run with disposable data, isolated from production CPA and
   client configuration. Start without the full graph/evolution feature set.
2. Replay a sanitized multi-client sample while preserving complete tool traces,
   original event times and project boundaries. Suppress repeated history.
3. Compare against ai-memory using the same questions: Chinese decisions, exact
   errors/paths, failed approaches, corrections and cross-client continuation.
4. Measure extraction and embedding calls, tokens, latency, memory and disk
   usage; do not adopt published quality or capacity claims as local evidence.
5. Verify source attribution, contradictory updates, deletion/index cleanup,
   job replay, uncertain post-model outcomes and backup restoration.
6. Verify useful recall in two actual clients. Returned knowledge must cite
   source sessions and remain historical data rather than executable authority.

Choose ongoing SDK/service integration, a separate MemHub deployment, or selective
implementation borrowing only after these checks. The existing archive remains
the recovery and reprocessing source throughout the experiment.

## Pinned references

- [Hy-Memory overview](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/README.md)
- [License](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/LICENSE)
- [SDK client](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/hy_memory/client.py)
- [SDK configuration](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/hy_memory/config.py)
- [Vector backend factory](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/hy_memory/data/vector_store.py)
- [Coding scenes](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/docs/scene_coding_usage.md)
- [Coding lexical reranking](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/hy_memory/coding/lexical.py)
- [Code revision chain](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/hy_memory/coding/chain_store.py)
- [MemHub overview](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/memhub/README.md)
- [MemHub architecture](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/memhub/docs/ARCHITECTURE.md)
- [MemHub SDK boundary](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/memhub/MEMORY.md)
- [Governance implementation](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/memhub/apps/core/src/one_memory/memory_governance.py)
- [MemHub privacy](https://github.com/Tencent-Hunyuan/Hy-Memory/blob/f79770cdb098534d2444178347ad5adc7c473135/memhub/PRIVACY.md)
- [Current storage interface](../internal/storage/storage.go)
