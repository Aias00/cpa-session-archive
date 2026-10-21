# Memory framework comparison

Reviewed: 2026-10-20. Local architecture baseline: `eaf50ca`.

This assessment uses pinned public documentation, licenses and selected production
source. The frameworks were not installed or executed, and no private archive
content was sent to them. Recommendations are hypotheses for a bounded evaluation,
not measured framework performance or a production migration decision.

## Recommendation

Keep the current collector and raw SQLite archive. Improve the independent memory
service first: semantic candidate generation alongside lexical retrieval,
structured continuation, and source-backed derived claims. Borrow PowerContext's
artifact/handoff lifecycle and ai-memory's coding continuity patterns. Use
Hindsight as the first optional full memory-engine experiment; use Mem0 as a
smaller fact-memory integration comparison if that experiment is too expensive
or too broad.

The immediate retrieval gap is semantic matching, not a demonstrated database
limit. On the frozen synthetic corpus, diverse retrieval finds 48/56 supported
questions (85.7% hit@5), but paraphrases remain 0/8. The real owner-labeled corpus
is still pending. Changing SQL storage alone does not change literal matching
into paraphrase retrieval. See [the evaluation](recall-evaluation.md).

The production archive has approximately 142,000 sources. Sources, repeated
message occurrences, unique text chunks and synthesized facts are different
capacity units. Framework examples involving a few thousand wiki pages do not
establish capacity for every archived message occurrence.

## Reviewed versions and roles

Each project name links to the reviewed commit. Storage below describes that
implementation, not every historic release or hosted product.

| Framework | Useful ideas for this plugin | Actual persistence / vector path | Recommended role |
| --- | --- | --- | --- |
| [ai-memory](https://github.com/akitaonrails/ai-memory/tree/5519b89cc653bec4cfccbb770c6b4e5dcfcda759) | Typed handoffs, bounded startup brief, versioned pages, conditional writes, explainable multi-stream fusion | Git Markdown plus SQLite operational state/FTS5; stored-vector cosine scan | High-value coding-memory design reference; optional separate knowledge service, with generated pages kept outside approved Git knowledge |
| [PowerContext](https://github.com/oceanbase/powercontext/tree/0da4436d866b8580ca0153fa40785dc94119e9f1) | Source/Artifact/Trigger contracts, immutable artifact revisions, handoff acknowledgement/outcome, review inbox, shared context budget | SQLite, embedded seekdb, or OceanBase runtime backends | Highest-value reference for continuation and governed memory workflows; optional context service, without requiring OceanBase |
| [TencentDB-Agent-Memory](https://github.com/TencentCloud/TencentDB-Agent-Memory/tree/b8e13539fd973e2426e18db4c21e6b7303b4852f) | Layered conversation memory, reusable skills, asset versions/review and code knowledge organization | Local SQLite/files or Tencent Cloud VectorDB; experimental MongoDB path | Consider MemoryCore API if team assets/skills become a requirement; full Hub/proxy stack is broader than the present retrieval gap |
| [Hindsight](https://github.com/vectorize-io/hindsight/tree/a7ab53c84a847664d3377dfb7987d3e6bbaf4b17) | Multi-route retrieval, temporal/entity connections, source-backed facts, document provenance and derived observations | Default PostgreSQL/pgvector; local pg0 still runs PostgreSQL | First optional full memory backend to trial behind our adapter; adds database and model operations |
| [agentmemory](https://github.com/rohitg00/agentmemory/tree/14dfd6e133c03f482311dd32150696be4b9dbaca) | Typed coding observations, lexical/vector RRF, bounded handoff workflow, Git-commit-to-session links | iii-engine file KV by default; optional Redis; in-memory vector cosine scan | Borrow coding schemas and retrieval techniques; limited reason to add another collector/runtime |
| [memU](https://github.com/NevaMind-AI/memU/tree/42ef9fa3a8034f5817684cba23ef69fa70a58197) | Agent-authored readable drafts, isolated prepare/verify workspaces, incremental embedding | In-memory, SQLite JSON embeddings/Python cosine, or PostgreSQL/pgvector; no ANN index identified in reviewed schema | Useful file-memory workflow reference; lower priority as a backend because citation, concurrency and knowledge-authority work remains |
| [Mem0](https://github.com/mem0ai/mem0/tree/2c81af3bbe27d9bccb09c772d583df9e9be72ee5) | Fact extraction/deduplication, provider adapters, memory history and scoped metadata | Library defaults to Qdrant; server defaults to PostgreSQL/pgvector; separate SQLite history | Optional fact-memory component; simpler API integration than a full memory product, but requires our evidence and governance wrapper |

## What to adopt and what to adapt

### ai-memory

Its page-oriented coding memory and explicit handoff protocol closely match
cross-client continuation. Adopt typed continuation fields, bounded context
packing, compare-and-write updates and retrieval explanations. Reuse our existing
Git proposal path for knowledge promotion.

Its SQLite reader really combines lexical, entity, neighbor and vector streams
with reciprocal rank fusion (RRF). Vector selection scans matching scoped page
vectors; it is not an ANN index. The default local MiniLM model and `unicode61`
lexical processing do not establish Chinese/code retrieval quality.

The wiki is authoritative for its pages, but operational SQLite data is also
important: sessions, observations, handoffs, credentials, audit state and vectors
are not all recoverable from Markdown. Automated wiki consolidation must not
become another approved knowledge authority in our system.

Evidence:

- [Scoped vector scan and hybrid fusion](https://github.com/akitaonrails/ai-memory/blob/5519b89cc653bec4cfccbb770c6b4e5dcfcda759/crates/ai-memory-store/src/reader.rs#L5276).
- [Programmatic writes and handoffs](https://github.com/akitaonrails/ai-memory/blob/5519b89cc653bec4cfccbb770c6b4e5dcfcda759/docs/programmatic-memory.md).
- [Vector backend policy](https://github.com/akitaonrails/ai-memory/blob/5519b89cc653bec4cfccbb770c6b4e5dcfcda759/docs/vector-backend-policy.md).
- [Reindex recovery limits](https://github.com/akitaonrails/ai-memory/blob/5519b89cc653bec4cfccbb770c6b4e5dcfcda759/docs/lifecycle-ops.md#L1210).

### PowerContext

The useful abstraction is evidence becoming revisioned artifacts, with a
separate current-work handoff. Adopt the Work Contract -> Prepared Handoff ->
explicit Commit (durable Revision) -> Acknowledgement -> Task Outcome lifecycle
and distinguish facts, decisions,
temporary state, reusable experience and executable skills. Its shared byte
budget is a good model for a structured successor to our excerpt-only recap.

The framework can run with SQLite; evaluating it does not require migrating to
OceanBase. Its SQLite implementation includes FTS5 and scoped vector retrieval,
with scope filtering before candidate limits. A scope selects data, and
authorization must bind an authenticated identity to that scope; access control
is disabled by default in the reviewed server settings. Its review model can
permit an authorized agent to
approve its own candidate. Our existing rule requires a separate human decision
through reviewed Git knowledge, so that permission pattern must be adapted.

Its source journal rejects a different payload under the same
`(scope_id, source_type, source_id)` identity.
An adapter needs revision-aware source IDs and provenance. Forgetting removes
memory from active recall while preserving history; it does not provide our
general source deletion/invalidation contract.

Evidence:

- [Contracts, handoff, review and context budget](https://github.com/oceanbase/powercontext/blob/0da4436d866b8580ca0153fa40785dc94119e9f1/README.md).
- [SQLite index implementation](https://github.com/oceanbase/powercontext/blob/0da4436d866b8580ca0153fa40785dc94119e9f1/src/powercontext/builtin/persistence/sqlite/atomic_memory_index.py#L73).
- [Source identity and content conflicts](https://github.com/oceanbase/powercontext/blob/0da4436d866b8580ca0153fa40785dc94119e9f1/src/powercontext/builtin/persistence/sources.py#L101).
- [Lifecycle limits](https://github.com/oceanbase/powercontext/blob/0da4436d866b8580ca0153fa40785dc94119e9f1/docs/en/docs/workflows/architecture.md#L99).
- [Access defaults](https://github.com/oceanbase/powercontext/blob/0da4436d866b8580ca0153fa40785dc94119e9f1/src/powercontext/server/settings.py#L134) and [review permission checks](https://github.com/oceanbase/powercontext/blob/0da4436d866b8580ca0153fa40785dc94119e9f1/src/powercontext/server/dream_access.py#L198).

### TencentDB-Agent-Memory

Its distinction between conversation memory, atomic memories, scenarios and
persona is a useful layering reference. Skills and versioned team assets are
relevant later if the plugin expands beyond archive recall into reviewed
reusable workflows. These should remain generated candidates until approved.

The complete product includes MemoryCore, MemoryHub and a proxy. Our collector
already captures committed evidence, so an experiment should call a confirmed
MemoryCore intake/recall API instead of adding another capture proxy. Local
storage exists; Tencent Cloud storage is optional. MongoDB is explicitly
experimental in the reviewed version.

L1 retrieval can fuse independent lexical/vector candidates, but the inspected
path catches stream errors as empty results. Preserve explicit failure reporting
in our adapter. The L0 deletion endpoint does not cascade to all derived
L1/L2/L3 records, so source invalidation also remains our responsibility.

Shared-key authentication and strict-isolation checks are disabled by default
in the reviewed Core. Enabling namespace completeness checks does not bind
caller-supplied team/user/agent fields to an authenticated identity. Keep our
credential-bound scope and original-source checks in front of returned memory.

Evidence:

- [Product components and storage caveat](https://github.com/TencentCloud/TencentDB-Agent-Memory/blob/b8e13539fd973e2426e18db4c21e6b7303b4852f/README.md).
- [Published MemoryCore API contract](https://github.com/TencentCloud/TencentDB-Agent-Memory/blob/b8e13539fd973e2426e18db4c21e6b7303b4852f/MemoryCore/v3-api-memorycore-doc.md).
- [Actual backend factory](https://github.com/TencentCloud/TencentDB-Agent-Memory/blob/b8e13539fd973e2426e18db4c21e6b7303b4852f/MemoryCore/src/core/store/factory.ts#L48).
- [Candidate fusion and error handling](https://github.com/TencentCloud/TencentDB-Agent-Memory/blob/b8e13539fd973e2426e18db4c21e6b7303b4852f/MemoryCore/src/core/tools/l1-candidate-recall.ts#L175).
- [L0 deletion](https://github.com/TencentCloud/TencentDB-Agent-Memory/blob/b8e13539fd973e2426e18db4c21e6b7303b4852f/MemoryCore/src/gateway/v2-router.ts#L1000).
- [Isolation configuration](https://github.com/TencentCloud/TencentDB-Agent-Memory/blob/b8e13539fd973e2426e18db4c21e6b7303b4852f/MemoryCore/src/utils/env-config.ts#L192).

### Hindsight

This is the most relevant optional full-engine experiment for source-backed
memory: document/chunk identity, extracted facts, accumulated observations and
semantic/lexical/graph/temporal retrieval offer more than storing embeddings.
Return recall evidence through our adapter; a generated reflection is a derived
answer, not an original conversation excerpt.

Default persistence remains PostgreSQL/pgvector. Embedded pg0 reduces setup but
does not eliminate a PostgreSQL runtime, its resource use or backup requirements.
New extension points do not establish that an arbitrary database can replace
the complete control plane. Retention/extraction, embedding and reranking have
separate model/latency costs. Declared Apple Silicon support is not a local
capacity measurement; English-oriented defaults also need a Chinese/code trial.

Its internal provenance and transactional delete/reingest facilities improve
adapter feasibility. File cleanup after database commit is best effort; this is
not atomic erasure across all stores. Its document IDs and bank scopes still
need mapping to our archive identity, source revision, authorized scope and exact
citations. The default tenant extension has no authentication, and the simple
API-key extension uses one shared schema.

Begin an experiment with `HINDSIGHT_API_LLM_PROVIDER=none`, a selected
multilingual embedder and bounded retrieval. This forces `chunks` mode, disables
observation consolidation and makes reflection unavailable. Selecting `chunks`
alone does not disable every LLM feature. Add observations/reflection only
after measuring useful gains. The normal extraction configuration permits
partial extraction errors by default; enable strict failure handling and track
coverage rather than treating an accepted retain as complete indexing.

Evidence:

- [Server, PostgreSQL and supported platforms](https://github.com/vectorize-io/hindsight/blob/a7ab53c84a847664d3377dfb7987d3e6bbaf4b17/README.md).
- [Runtime and model defaults](https://github.com/vectorize-io/hindsight/blob/a7ab53c84a847664d3377dfb7987d3e6bbaf4b17/hindsight-api-slim/hindsight_api/config.py#L1193).
- [Recall pipeline and provenance options](https://github.com/vectorize-io/hindsight/blob/a7ab53c84a847664d3377dfb7987d3e6bbaf4b17/hindsight-api-slim/hindsight_api/engine/memory_engine.py#L9293) and [RRF](https://github.com/vectorize-io/hindsight/blob/a7ab53c84a847664d3377dfb7987d3e6bbaf4b17/hindsight-api-slim/hindsight_api/engine/search/fusion.py#L29).
- [Document source visibility](https://github.com/vectorize-io/hindsight/blob/a7ab53c84a847664d3377dfb7987d3e6bbaf4b17/hindsight-api-slim/hindsight_api/engine/source_scope.py).
- [Document deletion and observation invalidation](https://github.com/vectorize-io/hindsight/blob/a7ab53c84a847664d3377dfb7987d3e6bbaf4b17/hindsight-api-slim/hindsight_api/engine/memory_engine.py#L11321).
- [Tenant implementations](https://github.com/vectorize-io/hindsight/blob/a7ab53c84a847664d3377dfb7987d3e6bbaf4b17/hindsight-api-slim/hindsight_api/extensions/builtin/tenant.py#L8), [partial-failure default](https://github.com/vectorize-io/hindsight/blob/a7ab53c84a847664d3377dfb7987d3e6bbaf4b17/hindsight-api-slim/hindsight_api/config.py#L2170) and [no-LLM configuration](https://github.com/vectorize-io/hindsight/blob/a7ab53c84a847664d3377dfb7987d3e6bbaf4b17/hindsight-api-slim/hindsight_api/config.py#L4427).

### agentmemory

Borrow typed coding observations, separation of observations from synthesized
memory, lexical/vector fusion, and links from a code commit to the session that
produced it. Git evidence from a remote workstation still requires explicit
client-side evidence; a proxy cannot infer the current working-tree state.

The actual default is iii-engine file KV, with periodic persistence and
in-memory indexes. Its vector index traverses stored vectors. Neither is a
reason to replace our existing SQLite projection. Caller-supplied project and
agent filters are not an authorization boundary, and observation-ID citations
do not implement our original-source revision and Unicode range contract.

Evidence:

- [Default persistence](https://github.com/rohitg00/agentmemory/blob/14dfd6e133c03f482311dd32150696be4b9dbaca/iii-config.yaml#L9).
- [Hybrid ranking](https://github.com/rohitg00/agentmemory/blob/14dfd6e133c03f482311dd32150696be4b9dbaca/src/state/hybrid-search.ts).
- [Vector scan](https://github.com/rohitg00/agentmemory/blob/14dfd6e133c03f482311dd32150696be4b9dbaca/src/state/vector-index.ts#L99).
- [Citation payload](https://github.com/rohitg00/agentmemory/blob/14dfd6e133c03f482311dd32150696be4b9dbaca/src/functions/verify.ts#L17).
- [Commit/session workflow](https://github.com/rohitg00/agentmemory/blob/14dfd6e133c03f482311dd32150696be4b9dbaca/plugin/skills/commit-context/SKILL.md#L27).

### memU

The reviewed version is an agent-driven Wiki/skill workflow. Its Python
MemoryService is explicitly embedding-only; a host agent does the synthesis.
Borrow isolated prepare/verify workspaces and readable drafts. Route approved
knowledge through our existing Git workflow rather than automatically
activating another Wiki.

SQLite stores embeddings as JSON and scores scoped segments in Python.
PostgreSQL supports pgvector distance ordering, but no HNSW/IVFFlat index was
identified in the reviewed schema. Metadata `where` filters are not credential
authorization. Its resource/file/segment objects do not implement our citation
or source-revision contract.

Its developer documentation explicitly describes uncertain commit outcomes and
possible last-writer overwrite: there is no ETag/base-revision merge in that
workflow, and independent repository commits can leave partial results. These
are material adapter responsibilities, not details to inherit as guarantees.

Evidence:

- [Embedding-only service and scope filters](https://github.com/NevaMind-AI/memU/blob/42ef9fa3a8034f5817684cba23ef69fa70a58197/src/memu/app/service.py#L24).
- [Cosine retrieval repository](https://github.com/NevaMind-AI/memU/blob/42ef9fa3a8034f5817684cba23ef69fa70a58197/src/memu/database/repositories/recall_file_segment.py#L18).
- [Retrieval and commits](https://github.com/NevaMind-AI/memU/blob/42ef9fa3a8034f5817684cba23ef69fa70a58197/src/memu/app/agentic.py).
- [Retry and concurrency limits](https://github.com/NevaMind-AI/memU/blob/42ef9fa3a8034f5817684cba23ef69fa70a58197/docs/developer.md#L285).

### Mem0

Borrow fact extraction/deduplication and provider boundaries when structured
preferences and facts become useful. The active OSS v3 extraction pipeline is
ADD-only; automatic LLM UPDATE/DELETE reconciliation from older descriptions
must not be assumed. Explicit mutation APIs still exist. Raw tool traces and
failed approaches must remain available separately; a fact extractor is not a
transcript store. `infer=False` provides a direct embedding path for a smaller
initial experiment.

The reviewed OSS v3 retrieval obtains candidates from vector top-k and then
applies lexical/entity rescoring. That path is not an independent lexical union
capable of rescuing every exact error/path miss. Its message normalization also
ignores tool roles in the inspected extraction path. Preserve our lexical
branch and tool/source mappings if using it.

Hosted product benchmarks and features must not be attributed automatically to
the OSS implementation. A configurable vector provider covers vector memory,
not the archive's records, CAS, snapshots, source revisions and change ordering.
Mem0 memory IDs/history still require our source-to-derived-memory ledger and
deletion propagation. Vector deletion, SQLite history and best-effort entity
cleanup are separate operations, not a cross-store transaction. The self-hosted
server has authentication, but its ID/filter routes still need our project
authorization wrapper.

Evidence:

- [OSS/product distinction](https://github.com/mem0ai/mem0/blob/2c81af3bbe27d9bccb09c772d583df9e9be72ee5/README.md).
- [Direct embedding and active extraction](https://github.com/mem0ai/mem0/blob/2c81af3bbe27d9bccb09c772d583df9e9be72ee5/mem0/memory/main.py#L881).
- [Semantic candidate construction](https://github.com/mem0ai/mem0/blob/2c81af3bbe27d9bccb09c772d583df9e9be72ee5/mem0/memory/main.py#L1642).
- [Message role handling](https://github.com/mem0ai/mem0/blob/2c81af3bbe27d9bccb09c772d583df9e9be72ee5/mem0/memory/utils.py#L61).
- [Delete implementation](https://github.com/mem0ai/mem0/blob/2c81af3bbe27d9bccb09c772d583df9e9be72ee5/mem0/memory/main.py#L2114).
- [Server routes](https://github.com/mem0ai/mem0/blob/2c81af3bbe27d9bccb09c772d583df9e9be72ee5/server/main.py#L443).

## Storage selection

| Layer / requirement | Recommendation now | Reason to reconsider |
| --- | --- | --- |
| Raw archive, CAS, committed change feed | Keep `archive.sqlite` and collector ownership | Measured archive write/snapshot/backup limits; any replacement must implement the entire archive backend contract |
| Current lexical retrieval and occurrence/citation metadata | Keep independent `memory.sqlite` | Measured query/index/backup pressure after profiling |
| New semantic vectors | Evaluate a derived local index over unique text first; select the implementation from measurements | Real gains plus excessive scan latency, disk usage or operational cost justify a dedicated vector store |
| Full Hindsight backend | PostgreSQL/pgvector, with our adapter ledger | Adopt only if its measured quality and lifecycle features justify its extra runtime/model cost |
| Mem0 backend | Select one supported vector provider for the experiment | Provider support is not proof of transactional integration, local packaging or backup compatibility |
| Reviewed reusable knowledge | Keep canonical reviewed Git repositories | Framework-generated pages remain drafts/derived claims |
| OceanBase/seekdb or cloud databases | Conditional options, not immediate migration work | Demonstrated multi-user/distributed/multimodel need or a framework requirement that justifies operations |

SQLite vector storage is viable in principle, but a vector extension or pgvector
column does not itself prove ANN indexing or acceptable filtered-query latency.
Do not embed every repeated request occurrence: share computed embeddings where
the normalized text and model fingerprint match, while preserving each source
occurrence and its authorization/citation identity.

## Adapter contract

```text
collector -> authoritative raw archive + authenticated MemoryFeed
                                           |
                             durable adapter jobs / source ledger
                                           |
                           selected memory engine / derived indexes
                                           |
                           scoped Reader -> existing HTTP and MCP

generated knowledge candidates -> existing Git proposals -> human review
```

An external engine is not a direct database driver replacement. Implement a
concrete `Projection`/`Reader` adapter only after selecting one experiment;
preserve the [existing boundaries](memory-architecture.md) and avoid introducing
a speculative registry for all seven frameworks.

Required adapter behavior:

1. Bind persistent archive identity and observed monotonic watermarks. Track
   input source IDs/revisions and backend object IDs, including one-to-many and
   many-to-one provenance.
2. Normalize committed evidence, retain original event time/tool attribution,
   and suppress replayed history without collapsing independent citations.
   Model output remains an attributed claim.
3. Keep durable pending/applied/deletion/uncertain operation state. For an
   external API, our database and its database do not share a transaction:
   recover uncertain outcomes using stable operations and reconciliation before
   recording them as applied. An accepted HTTP job is not completed projection.
4. Enforce credential scope during candidate generation and live source
   verification before serving evidence. Backend namespaces, arbitrary metadata
   filters and model-selected bank IDs are not sufficient authorization.
5. Return our original normalized citations. Verify current source revisions;
   invalidate derived memories on source deletion, changes or moves. A failed
   source request must never be interpreted as deletion or an empty archive.
   Do not silently promote a multi-source claim with missing support.
6. Track embedding provider, model/version or artifact fingerprint, dimensions,
   normalization and chunking version. Rebuild incompatible vectors instead of
   comparing them merely because dimensions match.
7. Separate operational memory from reviewed Git knowledge. Model workers may
   propose corrections/skills, but cannot approve or activate them. Decay and
   compaction of derived memory do not authorize deletion of raw evidence.

## Prioritized plan

| Priority | Deliverable | Acceptance evidence |
| --- | --- | --- |
| P0 | Complete the owner-labeled real question corpus; retain the frozen synthetic corpus | Chinese paraphrases, exact errors/paths, old decisions, failed approaches, updates, negative and cross-project cases; unchanged frozen labels/hash |
| P1 | Explicit hybrid mode in the existing projection, combining lexical and semantic candidates with diversity and bounded ranking | Reproducible paraphrase gain, no exact-query regression, zero citation/scope violations; measured M4 p50/p95, indexing lag, memory/disk and embedding cost |
| P2 | Structured cited continuation and derived claim lifecycle | Task/current state/decisions/failed attempts/next steps, observation time and evidence references; correction/supersession tests; Git promotion requires human review |
| P3 | Isolated Hindsight proof of concept; Mem0 comparison only if useful | Same sanitized corpus/questions; source mapping, delete/change/replay/outage/restore tests; total model/runtime cost and maintainability compared with P1 |
| P4 | Choose a persistent backend or dedicated vector index from measured need | Bounded scope-filtered latency and resource use, reproducible rebuild, coordinated backup/restore and a documented rollback path |

The current `literal` and `diverse` modes remain useful baselines. Structured
continuation should state when evidence was observed; it cannot claim current
workspace/test status solely from an old conversation. Each experiment uses
disposable data and explicit model/provider configuration before any production
import is considered.

## Licensing and reuse

| Project | License at the reviewed commit |
| --- | --- |
| ai-memory | [MIT](https://github.com/akitaonrails/ai-memory/blob/5519b89cc653bec4cfccbb770c6b4e5dcfcda759/LICENSE) |
| PowerContext | [Apache-2.0](https://github.com/oceanbase/powercontext/blob/0da4436d866b8580ca0153fa40785dc94119e9f1/LICENSE) |
| TencentDB-Agent-Memory | [MIT](https://github.com/TencentCloud/TencentDB-Agent-Memory/blob/b8e13539fd973e2426e18db4c21e6b7303b4852f/LICENSE) |
| Hindsight | [MIT](https://github.com/vectorize-io/hindsight/blob/a7ab53c84a847664d3377dfb7987d3e6bbaf4b17/LICENSE) |
| agentmemory | [Apache-2.0](https://github.com/rohitg00/agentmemory/blob/14dfd6e133c03f482311dd32150696be4b9dbaca/LICENSE) |
| memU | [Modified Apache-shaped license text](https://github.com/NevaMind-AI/memU/blob/42ef9fa3a8034f5817684cba23ef69fa70a58197/LICENSE.txt#L67); README labels Apache-2.0, but the actual grant/definitions differ from the standard text |
| Mem0 | [Apache-2.0](https://github.com/mem0ai/mem0/blob/2c81af3bbe27d9bccb09c772d583df9e9be72ee5/LICENSE) |

Design references and source copying are different reuse paths. Preserve the
applicable license, attribution, modification notices and NOTICE material when
copying or porting code. For memU, review and preserve the exact text rather than
assuming the badge identifies the complete terms. Database distributions,
iii-engine, model artifacts, native dependencies and hosted services have their
own terms; a framework license does not establish those terms.
