# Third-party notices

Native-history improvements reference and adapt the Codex rollout filename and
parent-source parsing rules in `yetone/magpie` (`internal/sessions/sessions.go`
and `internal/sessions/codex.go`). Message classification and unchanged-file
verification were informed by its session readers. Magpie is MIT licensed,
Copyright (c) 2026 yetone. The license is retained in `magpie-LICENSE`.

The Magpie module and its dependencies are not linked into this application.
The importer uses the installed `zstd` program for compressed files, and retains
complete decoded source records rather than Magpie's bounded display excerpts.

Additional native adapters reference Magpie commit `94919109`:
`internal/sessions/{qoder,workbuddy,opencode,zcode,transcript_opencode}.go`.
These references informed client discovery, SDK event shapes and v1/v2 table
selection. Database capture and archive checkpointing use this fork's existing
staging/CAS/import contracts, with immutable source-message revision envelopes.
