# CPA Chat Completions extraction provider

This provider is development-only. Protocol regressions use public synthetic
evidence and `httptest` servers; offline replay uses saved public-corpus inputs
and responses. These checks do not contact a real model endpoint or use private
conversations, credential files, client configuration, or live knowledge
repositories. Implementing this adapter does not enable it or deploy it.

## Official protocol and local policy

The implementation was checked against these fetched official OpenAI pages:

- [Chat Completions create reference](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create): request messages, response format, completion limits, metadata, finish reasons, and usage fields.
- [Structured Outputs](https://developers.openai.com/api/docs/guides/structured-outputs): JSON mode versus schema enforcement, refusal handling, and incomplete output.
- [API error codes](https://developers.openai.com/api/docs/guides/error-codes): rate limits and other 429 rejection classes.

OpenAI Docs establishes the OpenAI protocol, not that every CPA model route
implements each field. Compatibility with an explicitly configured real model
remains separate acceptance work; the synthetic tests do not prove it.

The request uses non-streaming Chat Completions, one choice,
`response_format: {"type":"json_object"}`, and an explicit model supplied by the
operator. JSON mode guarantees JSON syntax rather than conformance to an
application schema. This adapter therefore validates its candidate schema
locally, including unknown fields and duplicate keys. It does not silently
fall back to free text, another model, or another response format. It does not
send tools or execute response content.

`max_completion_tokens` is the explicit request's output budget. The official
reference defines it to include visible output and reasoning tokens, and marks
the older `max_tokens` parameter deprecated. A provider or model route that
rejects this parameter is incompatible with this configured adapter; it is not
retried using a different parameter.

## Request boundary and recursion tag

`NewHTTPProvider` accepts a literal-loopback HTTP origin, with optional `/v1`
base path. It sends to `/v1/chat/completions`. Hostnames, non-loopback addresses,
HTTPS, user information, queries, fragments, other paths, redirects, and system
proxies are rejected or bypassed. The default total timeout is thirty seconds;
the caller can impose a shorter context deadline. The provider makes no
automatic retries. The local CPA origin does not imply the configured model is
local: CPA controls its selected route, which this adapter does not change.

The fixed system message treats excerpts and existing knowledge as untrusted
evidence. The user message contains their JSON serialization, preserving exact
quotes and target ID/revision pairs. The provider sends `store: false` as a
protocol option; this is not a claim about CPA's own archive retention.

The request contains both metadata and CPA's existing extraction marker:

```text
metadata: {"request_kind":"memory-extraction"}
X-Codex-Turn-Metadata: {"request_kind":"memory-extraction"}
```

The header is a CPA integration convention, not an official OpenAI header.
CPA's existing metadata capture maps it to `RequestKind` and the `request.kind`
facet. Internal-source filtering uses this classification to keep extraction
requests out of later extraction and ordinary source recall. Merely adding an
arbitrary unmapped body field would not establish that protection. The exact
header and body tag are covered by the HTTP fixture test.

The configuration fingerprint binds the normalized endpoint, explicit model,
fixed prompt, extraction version, and candidate-schema version. It excludes the
token and timeout. A fingerprint is configuration provenance, not proof of a
provider's model weights or equivalent behavior behind an unchanged model name.

## Candidate and evidence contract

The JSON result has exactly one root field, `candidates`, with at most eight
items. Each item requires `kind`, `title`, `statement`, `attribution`, `supports`,
`action`, and `targets`. Titles contain 3–160 Unicode characters; statements
contain 10–3,500, leaving room for the downstream attribution prefix within the
canonical proposal's 4,000-character limit.

Kinds are `decision`, `fact`, and `experience`. Attributions are
`user_requirement`, `user_report`, `assistant_suggestion`, `assistant_report`,
and `execution_result`. Actions are `new`, `corrects`, `supersedes`,
`conflicts_with`, and `duplicate`. Neither a kind nor an action gives the model
approval authority.

Every support names a supplied `excerpt_id` and quotes a nonempty unchanged
substring of that excerpt. A new candidate has no targets; every other action
has at least one. Every target must match an exact supplied existing entry's
`id` and `revision`. Supports and targets are each capped at eight. Translated or
rewritten quotes, invented targets, missing fields, type substitutions, unknown
fields, duplicate keys at any JSON depth, and trailing JSON are rejected.

The downstream report builder independently checks source revisions, fixed
speaker attribution, scope, and eligibility of relation targets. Its output is
still a human-review candidate. A grounded quote does not mechanically prove
that a generated statement is entailed by it, that a suggestion was accepted,
or that two claims semantically conflict.

## Usage, reservation, and failures

The response must contain integer, nonnegative `prompt_tokens`,
`completion_tokens`, and `total_tokens`, with the total equal to their sum.
OpenAI documents usage as an optional response field; requiring it here is a
stricter local accounting policy. Missing or malformed usage must not be treated
as zero. Optional usage breakdowns, when present, are checked against their
supported fields and integer counts.

Version 2 explicitly accepts `usage.prompt_tokens_details.cached_creation_tokens`
as a nonnegative integer. This is an observed CPA compatibility extension, not a
field guaranteed by the official OpenAI reference. Negative, fractional, null,
boolean, string, or case-aliased values are rejected, and unrelated unknown
fields remain rejected. This count does not replace the required top-level
prompt/completion/total usage accounting.

The first real public-corpus gate on 2026-10-21 was rejected by version 1's
strict response schema. A subsequent single-source public protocol probe
established a `stop` finish, valid candidate JSON, and this additional zero-valued
CPA field. The compatibility fix changes the schema version to
`source-bound-candidates-json-v2`, producing a new configuration fingerprint.
The original ledger remains `uncertain`; the change does not clear its
reservation or automatically replay the call. The next public-corpus gate under
version 2 exposed a second CPA response extension.

Version 3 explicitly accepts the optional choice field `native_finish_reason`
as a nonempty string. The observed public response supplies `"stop"`; a route
may retain a different native stop label while its normalized `finish_reason`
must still be exactly `"stop"`. A native `"length"` reason is rejected as
truncation even when normalized `finish_reason` is `"stop"` and the content is
valid JSON. That check also rejects surrounding whitespace and case variants
of `length`. Null, numeric, boolean, array, object, empty and whitespace-only
values are rejected. Case-aliased keys and all other unknown fields remain
rejected.

The schema version is now `source-bound-candidates-json-v3`, so its configuration
fingerprint differs from both earlier profiles. Offline replay of the saved
version 2 public response produced four source-bound candidates with reported
usage of 1,238 input and 322 output tokens. The saved response SHA-256 is
`9dadd39b1cd1e46af301c23407ea85e8b1940c1e3a68ed51521f06071e234e30`.
The replay opened its ledger read-only, preserved its bytes, and left its one
uncertain job and four pending jobs unchanged. It made no model call. Full
public-corpus acceptance was then run under the new profile, as recorded in
[the batch verification](memory-extraction.md#verified-development-acceptance-2026-10-21).

The local input reservation estimates the bytes of the actual serialized
request plus 512 units of template overhead. It includes the fixed prompt,
metadata, escaped evidence, and supplied existing knowledge. This is a
conservative budgeting rule, not an exact tokenizer or invoice. Nested JSON
escaping can enlarge a source window, so its raw byte limit alone does not
establish that it fits a token reservation. The runner checks the actual
estimate before dispatch and must keep measured usage within the reservation.

Only a single assistant choice finishing with `stop`, without refusal or tool
calls, can return candidates. Truncation, refusal, malformed successful output,
missing usage, response-size overflow, transport failure, and timeouts return
sanitized `CallError` codes with `Uncertain: true`. This conservative label can
mean that the request was processed and spent tokens even though no usable
result was returned. It must not trigger an unattended replay or a zero-cost
assumption.

Successful HTTP responses that fail parsing report only a fixed stage code:
`completion_truncated`, `completion_refused`, `usage_invalid`, `schema_invalid`,
or `bindings_invalid`. The codes distinguish incomplete output, refusal,
accounting failures, response shape failures, and evidence/target mismatches.
They contain no upstream field names, values, response text, or partial usable
result. Every such failure remains uncertain and non-retryable without operator
reconciliation; a more specific diagnostic does not refund a reservation.

A structured 429 rate-limit rejection can be marked retryable and non-uncertain.
Quota rejections are not automatically retryable; unrecognized error bodies and
5xx responses remain uncertain. The adapter never performs that retry itself.
The task ledger owns attempt limits, reservations, backoff, and explicit
operator reconciliation. Provider error text omits upstream bodies, input text,
credentials, model responses, and credential-bearing URLs.

Requests are capped at 2 MiB and responses at 1 MiB. These limits and the
eight-candidate cap bound the protocol; they do not measure extraction quality,
real-model latency, semantic correctness, or owner acceptance.
