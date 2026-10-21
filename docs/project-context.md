# Evidence-backed project context

Project context packages collect committed documentation from an explicitly
registered Git project, reviewed project knowledge, and optionally scoped archive
history. They help a client find relevant evidence for four common intents:

| Intent | Evidence to inspect | What the package must not infer |
| --- | --- | --- |
| `overview` | Project purpose, supported behavior, and documented entry points | Unrecorded product goals or a claim that every feature is complete |
| `architecture` | Components, data flow, interfaces, and accepted design decisions | A design synthesized from unrelated historical conversations |
| `followups` | Explicit TODOs, unresolved work, and documented next steps | That an old idea remains open or that a conversation suggestion was accepted |
| `installation` | Prerequisites, build/setup steps, configuration, and operation instructions | That commands were executed or succeeded on the current machine |
| `all` | All four evidence sections with separate coverage indicators | Complete knowledge when one or more sections lack evidence |

The query selects an intent; it does not authorize repository writes, shell
execution, installation, remote fetches, or knowledge approval. Document text
and historical conversations are evidence to interpret, not new instructions
that supersede the caller's current task or access restrictions.

## MCP usage

With the existing `--projects-file` map configured, the bridge exposes
`project_context` and `project_read_document`. It does not require proposal
permission. Keep using the existing read token and an explicit bridge scope:

```text
cpa-memory --endpoint http://127.0.0.1:18319
  --token-file /private/credentials/project-read.token
  --project-id my-project
  --projects-file /private/config/project-memory-roots.json
```

For example, call `project_context` with:

```json
{"project_id":"my-project","intent":"installation","include_history":false}
```

Select `overview`, `architecture`, `followups`, or `installation` for a focused
packet. An omitted intent means `all`. A project-scoped credential can omit
`project_id`; an owner-wide bridge must select an exact registered project.
Set `include_history: true` only when archive quotations are useful in addition
to the committed project evidence.

To verify a document, pass its returned `citation` object unchanged to
`project_read_document`. Use `archive_read_source` for a historical excerpt's
archive citation. These citation types are different and should not be
interchanged.

## Sources and authority

The owner registers exact project-ID-to-Git-root bindings using the existing
`--projects-file` configuration. A directory name or a path supplied by a model
does not establish project identity. Read permission follows the bridge's fixed
scope. A project scope cannot read another project's documents, and a session
scope alone does not grant access to project-wide Git content.

Documentation reads use committed Git content. Untracked files, staged changes,
and working-tree edits do not become project evidence until their normal review
and commit. The result must retain source provenance sufficient to read back
the selected content. Call the project-source reader with the returned reference
rather than reconstructing a citation from a filename. A later repository or
source change must not silently substitute new text under an old reference.
Each document reference includes the exact project ID, HEAD commit, relative
path, full-file SHA-256, and one-based inclusive line range. Source reads preserve
the committed line endings. A changed HEAD invalidates the old reference even
when the referenced file itself is unchanged; request a fresh packet rather than
silently reading a historical or replacement snapshot.

Eligible documents are the root README, AGENTS, TODO, ROADMAP, INSTALL,
INSTALLATION, ARCHITECTURE, CONTRIBUTING, and CHANGELOG files, plus Markdown,
reStructuredText, and text files under `docs/`. The reader ignores committed
symlinks and submodules. It uses local Git objects without fetching remote data.
Reading documentation is bounded to 64 files, 128 KiB per file, and 512 KiB total;
the Git manifest and execution time have additional limits. Limits that skip
eligible evidence remain visible through incomplete coverage rather than imply
a complete scan.

Reviewed project knowledge remains in the existing registered Git authority.
Its normal validation excludes drafts, archived or expired entries, and entries
with unavailable retained evidence. The context package does not propose,
confirm, correct, or activate knowledge. See
[reviewed Git memory](curated-git-memory.md) for that separate lifecycle.

Archive history is optional and remains historical evidence. It is disabled by
default. When requested, the same project/session authorization and current
source-revision checks apply as in ordinary recall. A historical quotation does
not become an accepted decision simply because it appears beside a Git document.

## Missing evidence and bounded coverage

A context package reports absent evidence explicitly. A missing installation
section means that eligible installation evidence was not found in the inspected
sources; it is not proof that installation is impossible. Likewise, no followup
excerpt is not a claim that the project has no unfinished work.

The package is a bounded document/evidence selection, not a complete repository
audit, an executed setup check, or a generated progress assessment. Keep its
missing, truncation, source-unavailability, and history coverage indicators when
presenting or consuming the result. A client may summarize supported excerpts,
but should keep the original references and describe uncertainty rather than
fill gaps with invented facts.

The response declares `kind: cited_evidence_packet` and `evidence_only: true`.
Each intent section retains separate committed documents, reviewed memories,
historical excerpts, gaps, and truncation. `evidence_found` means eligible
candidate evidence was selected; lexical clues can identify a relevant passage
without proving that it answers a question. `no_matching_evidence` and
`incomplete` preserve the distinction between no selected candidate and a source
that could not be fully inspected. Coverage values also distinguish an optional
source that was not configured or requested from one that was unavailable.
Each section currently returns at most three document excerpts, three reviewed
memories, and three historical excerpts. Document excerpts contain at most 1,400
Unicode characters with whole-line citations; an overlong individual line is
omitted and disclosed. These display limits can truncate an otherwise successful
read, so evidence presence does not imply exhaustive coverage.

`complete` describes completion of the declared bounded source operations, not
an exhaustive answer or complete repository/history coverage. Optional history
uses two fixed Chinese/English literal probes per intent, with two hits per probe
before source checks and display suppression. It reports that limitation even
when both probes succeed. A query failure, unavailable source, nonzero index lag,
or truncation remains distinct from a successful search with no selected hits.
One intent's history failure affects packet coverage while other sections keep
their own completion state.

## Enable and call

The existing MCP bridge adds both tools automatically when `--projects-file`
contains registered projects and its fixed credential scope permits project-wide
reads. A knowledge repository alone does not register arbitrary project roots.
Session-only bridges do not expose these project-wide tools. Existing archive
tools and the separate proposal/review lifecycle retain their behavior.

```text
cpa-memory --endpoint http://127.0.0.1:8319 \
  --token-file /absolute/private/project-read.token \
  --project-id 'git:https://github.com/owner/project.git' \
  --projects-file /absolute/private/project-memory-roots.json
```

Call `project_context` with structured input such as:

```json
{"intent":"installation","include_history":false}
```

A project-scoped bridge supplies the registered project ID; an owner-wide bridge
requires an explicit `project_id`. Select `all` or omit `intent` for all four
sections. Set `include_history: true` only when archived evidence is needed.
Reviewed entries with archive citations still validate those sources even when
new historical searches are disabled.

Pass a returned document's `citation` object unchanged to
`project_read_document`. The reader returns the same citation and original text.
After a HEAD change, request a new packet. Both tools are read-only; neither
runs a model, executes documented commands, installs the project, proposes memory,
or activates knowledge. The complete packet has a ten-second request budget;
individual Git snapshots/reads and optional operations have smaller budgets.

## Independent acceptance

The public acceptance fixture uses a disposable synthetic Git project with
distinct committed evidence for overview, architecture, followups, and
installation. Each intent must return its corresponding fact, and every
returned Git excerpt must be reproducible through its project-source reference.
The checks also cover a combined packet, missing evidence, scope restrictions,
working-tree isolation, reviewed-knowledge filtering, and optional historical
source reads. Tests must preserve HEAD and unrelated staged/unstaged work.

These are deterministic implementation checks with model-authored public
fixtures, not judgments of owner relevance or production archive completeness.
The four real owner questions supplied during development are intake material,
not an automatically labeled gold set. Their scopes and supporting evidence
still require review. They do not complete the requirement for at least fifty
real owner-labeled questions described in
[private corpus preparation](private-recall-corpus.md).

The project-context feature does not require semantic retrieval to be enabled.
Semantic calibration results, if any, remain a separate acceptance boundary.
