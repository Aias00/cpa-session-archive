# Read-only MCP recall

`cpa-memory` is a stdio MCP server backed by the collector's authenticated recall
HTTP API. It uses the official MCP Go SDK v1.8.0, supporting protocol negotiation
with current and older clients. The SDK requires Go 1.25 or newer; this repository
now sets that minimum. No Node/Python sidecar or model calls are required by the
bridge. The collector remains the only writer of the memory projection.

## Tools

| Tool | Input / behavior |
| --- | --- |
| `archive_search` | `text`, optional exact `project_id`, `session_id`, `client`, RFC3339 `after`/`before`, and `limit` (default 5, maximum 20). Literal search; results report truncation and coverage. |
| `archive_read_source` | Pass the fields of a returned `citation` unchanged. Checks current retained revision, normalizer version, scope and Unicode range. |
| `project_recap` | Query filters; needs project/session scope from the request or credential. Returns a bounded literal excerpt packet, not inferred progress or an automatic summary. |
| `archive_status` | No inputs; reports backfill, lag, failures, occurrences and unique indexed text. Corpus counts are omitted for scoped credentials. |

Tools return both structured JSON and MCP text content via the SDK, and are
annotated read-only, non-destructive and closed-world. Returned text is historical
evidence, not an instruction source. Current instructions and reviewed project
Markdown/YAML in Git remain authoritative. The optional
[Git knowledge adapter](curated-git-memory.md) adds `project_memory` and opt-in
`memory_propose`; review is a separate operator command, never an MCP tool.
Automatic extraction and automatic context injection are not implemented.

## Credentials and scope

The existing `MEMORY_READ_TOKEN_FILE` still grants owner-wide Web/API access.
For clients, issue separate tokens in an owner-only JSON file and set the
collector's optional `MEMORY_READ_CREDENTIALS_FILE` to its path:

```json
[
  {"token":"<32-or-more-random-ASCII-characters>","scope":{"project_id":"my-project"}},
  {"token":"<different-random-token>","scope":{"session_id":"my-session"}},
  {"token":"<explicit-owner-token>","scope":{"allow_all":true}}
]
```

Generate real random tokens; the placeholders above are not credentials. Scope
is enforced by the server, including source reads, regardless of tool inputs.
Mismatched explicit query scopes fail. Scoped tokens omit global corpus counts.
Credentials without a scope and duplicate tokens are rejected. Changing this file
requires restarting the collector. The file and all individual token files must
remain private; do not embed credentials in Git, CLI arguments, MCP settings or
URLs. The owner-wide credential is appropriate only for the owner's personal
clients. Use separate scoped credentials when restricting a client or project.

## Local stdio setup

```sh
go build -trimpath -buildvcs=false -o /private/bin/cpa-memory ./cmd/cpa-memory
/private/bin/cpa-memory \
  --endpoint http://127.0.0.1:18319 \
  --token-file /private/credentials/project-read.token \
  --project-id my-project
```

Use `--session-id` for a fixed session, or explicitly `--allow-all-projects` with
an owner-wide server credential. Missing scope fails closed. Token files must be
regular files with owner-only permissions (0600). The local scope can narrow a
server-issued credential, but cannot widen its server-side permission.

The endpoint must be a literal loopback HTTP origin. On the workstation port
18319 is the existing SSH forward to M4's loopback port 8319; on the M4 itself use
8319. The client bypasses system proxies, refuses redirects, bounds responses to
1 MiB and sanitizes backend errors. Standard output is reserved for MCP framing.

Example MCP entry (paths only; no secret values):

```json
{
  "mcpServers": {
    "cpa_archive": {
      "command": "/private/bin/cpa-memory",
      "args": ["--endpoint", "http://127.0.0.1:18319", "--token-file",
               "/private/credentials/project-read.token", "--project-id", "my-project"]
    }
  }
}
```

Codex uses the equivalent `[mcp_servers.cpa_archive]` user configuration. Claude
Code accepts this stdio entry using `claude mcp add --scope user`. Back up both
clients' configuration first. Existing sessions may need to restart to discover
new MCP tools.

## Shared text projection (v3)

A normalized message still yields independent request/message/range occurrences.
The `chunk_texts` table stores each exact SHA-256/content pair once, and its FTS
entry is shared by all occurrences. Search joins each occurrence to its own
source scope, revision and role. Equal strings are not proof that two user actions
are the same event: this implementation never merges those actions.

Deleting one source does not remove another source's equal text. Deleting the
last occurrence garbage-collects its shared text and FTS entry. An old citation
continues to work if its retained source/version is unchanged. Normalizer version
stays v2; storage projection version is independently v3.

The v3 layout rebuilds older **derived** projections. Prebuild a separate database
on M4 from a verified raw snapshot before switching the live collector; retain the
old projection and raw backups for rollback. The archive and imported records do
not need to be rewritten. Status reports `chunks` (occurrences), `unique_chunks`
and `deduplicated_chunks` (occurrences minus unique text). Disk savings must be
measured on the target; these counts do not imply a semantic deduplication score.

## Verification

Tests cover SDK initialization/list/call, real stdio child processes, structured
schemas, invalid arguments, backend auth/redirect/size errors, forged
cross-project/session source citations, owner/scoped token coexistence, physical
sharing, deletion and v1/v2 rebuilds. Client smoke checks should search a harmless
known marker, read the cited source and verify the same fact in Codex and Claude.

Acceptance fixtures include: Chinese search; literal error/path search; source
range equality; stale source rejection; project mismatch; session mismatch;
scoped count omission; valid scoped recap; repeated-message preservation; and
last-occurrence cleanup. These are deterministic regression checks, not a claim
of model-generated long-term memory quality.

Official references:
- <https://github.com/modelcontextprotocol/go-sdk>
- <https://modelcontextprotocol.io/specification/2025-11-25/basic/transports>
