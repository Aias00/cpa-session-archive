# Built-in local recall

The fork includes an opt-in Go + SQLite recall engine. It searches retained
request/response text and produces cited source excerpts without model calls,
embeddings or another runtime. The optional stdio bridge uses the official MCP
Go SDK. The archive remains the source
of evidence; `memory.sqlite` is a separate derived projection.

The [read-only MCP bridge](mcp-recall.md) now lets clients invoke search, source
reads and recaps directly. The Web page remains available. Curated memory
editing, semantic retrieval, native thread restoration and automatic context
injection are not implemented.

## Enable on a development collector

Build the collector normally; no FTS5 build tag is required:

```sh
go build -trimpath -buildvcs=false -o /tmp/cpa-session-collector ./cmd/cpa-session-collector
```

Create a private workspace and a dedicated random read credential. Do not reuse
or publish the CPA management key:

```sh
mkdir -p "$HOME/Library/Application Support/CPA Recall"
chmod 700 "$HOME/Library/Application Support/CPA Recall"
python3 - <<'PY'
import os, pathlib, secrets
path = pathlib.Path.home() / 'Library/Application Support/CPA Recall/read.token'
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'w') as stream:
    stream.write(secrets.token_urlsafe(36) + '\n')
PY
```

Start your collector using its existing archive path and settings, adding:

```sh
export MEMORY_ENABLED=true
export MEMORY_READ_TOKEN_FILE="$HOME/Library/Application Support/CPA Recall/read.token"
export MEMORY_LISTEN_ADDR=127.0.0.1:8319
# Optional: default is memory.sqlite alongside ARCHIVE_DB.
# export MEMORY_DB='/private/workspace/memory.sqlite'
/tmp/cpa-session-collector
```

Open <http://127.0.0.1:8319/> and enter the dedicated token. The page keeps it in
memory only; it does not put it in a URL, localStorage or sessionStorage. This
credential grants owner-wide read access to all archived projects on this
machine. Project/client filters narrow results; they are not access-control
rules. Do not distribute the credential to untrusted clients.

The separate recall listener rejects non-loopback binds and cross-origin API
requests. Do not expose the collector's unauthenticated ingestion listener.
For a Mac mini deployment, forward only the recall listener:

```sh
ssh -N -L 18319:127.0.0.1:8319 mac-mini-m4
```

Then open <http://127.0.0.1:18319/>. Enabling this feature in a local checkout does
not change the already installed Mac mini collector. Back up the original
archive before installing a new collector build.

## Search and evidence

Search accepts up to 256 Unicode characters and 16 whitespace-separated literal
terms. All terms must occur in the same text chunk. Results are ordered by source
request time, not model relevance. Unicode word tokens and CJK character/bigram
tokens use bundled SQLite FTS4; literal verification rejects token-only false
matches. Arbitrary Latin substrings inside a word and paraphrases are not a
semantic search feature. Punctuation-only queries use a bounded candidate scan.

At most 1,000 candidates and 100 results are considered per request (20 results
by default). Overlapping chunks from a single normalized message produce one
hit. The API and page report truncation; narrow the scope to retrieve more.
Chunks contain at most 1,600 Unicode characters with 256-character overlap;
indexed messages themselves have no UI tail/40 KB truncation.

Filters are exact `project_id`, `client`, `session_id`, inclusive `after` and
exclusive `before` (RFC3339 timestamps). Project scope is an explicit
`project.id` facet, otherwise `git:` followed by the exact `git.remote` facet.
Names and directory paths alone do not create a cross-device project identity.
Unknown sources stay unassigned. Remote aliases are not automatically merged.

Chat Completions, Responses (including Codex custom tools and agent messages)
and Claude Messages include text, tool arguments
and tool results. Unsupported media/protocols, missing payloads, truncated
records and incomplete streams are labeled partial. Opaque reasoning is omitted.
Gemini payload normalization is not implemented in this increment. Coverage
refers to retained CPA requests, not the entire native client history.

Each hit includes a request ID, retained revision SHA-256, payload side, message
pointer, normalizer version and Unicode rune range. JSON pointers identify
normalized text within a retained JSON value. SSE `sse:` locators are synthetic
event-group locators, not JSON pointers into the raw stream. Source reads repeat
the same versioned normalization and range check. They do not return private
opaque fields or silently substitute a new request body for an old citation.

Replaying a request ID does not add projection rows. Distinct identical messages
inside a request remain distinct. Shared history across different request
snapshots remains separately attributable; there is no text-hash-only merging
of user actions and no claim of a canonical native conversation transcript.

## API

Every data endpoint requires `Authorization: Bearer <dedicated read token>`:

| GET path | Parameters / output |
| --- | --- |
| `/v1/search` | `q`, optional exact scope/time filters and `limit`; hits, truncation and status |
| `/v1/source` | Fields from a hit's `citation`, including `normalizer_version`; checked excerpt and coverage issues |
| `/v1/recap` | Search filters; requires project or session; at most 8 KiB of cited literal excerpts |
| `/v1/status` | Backfill progress, checkpoint/watermark, lag, source/chunk counts, partial coverage and failures |

A recap is an excerpt packet, not an AI inference about decisions or successful
checks. Historical excerpts never override current instructions. No matching
evidence is reported explicitly. Source changes/deletions invalidate old reads
with HTTP 410; pending reindexing may temporarily hide an updated source.

## Recovery, privacy and operation

Existing archive triggers append changes in the same transaction as records.
An initial row-ID backfill is resumable, followed by ordered change-log replay.
Projection changes and live checkpoints commit atomically; an interrupted
backfill page safely replays. One collector-owned worker runs at a time. Failed
work leaves the cursor pending, retries once per second and records a sanitized
error/counter. Read-time revision validation suppresses stale/deleted/moved
sources even before the worker catches up.

This does not make the plugin queue or collector HTTP 202 durable. Records lost
before the archive commit cannot be recovered by the recall worker.

Projection v3 shares exact text and FTS entries while retaining every source
occurrence; see the [MCP/shared-text guide](mcp-recall.md). The projection stores
private plaintext. The main file is created/restricted to
0600. Protect its directory and backups. Deletion immediately blocks API recall,
but secure erasure from SQLite free pages, WAL files or backups is not promised.
Do not delete only the projection to delete original evidence. The projection
can be rebuilt by stopping the collector, moving its database and WAL/SHM files
aside together, then restarting against the same archive. Keep the original
archive and its committed change log intact. There is no curated user state in
this first projection schema.

## Validation of this increment

- Full Go test suite, race detector, `go vet`, formatting and native release
  binary builds passed on the development Mac (Darwin arm64).
- A synthetic 30-query literal regression corpus retrieved each expected source
  in the top five. This is not a labeled real-user/semantic quality evaluation.
- A 10,000-chunk local benchmark completed 100 queries at approximately 0.23 ms
  per query on an Apple M4 Pro. This is a microbenchmark mean, not Mac mini p95,
  and does not include concurrent live capture or network/UI latency.
- A disposable real collector and Chromium smoke check covers ingestion,
  source reads, Web search/recap, text injection safety, restart and deletion.

Normalizer v2 adds Codex custom tool calls/results and attributed agent messages.
Opening a v1 projection atomically resets its derived corpus and checkpoints for
rebuild; old v1 citations return unavailable rather than changing meaning.

Deployment status is recorded in [the Mac mini runbook](mac-mini-deployment.md).
MCP protocol and client verification are recorded in the deployment runbook.
