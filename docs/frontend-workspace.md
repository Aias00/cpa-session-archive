# React session workspace

The session UI uses React, Vite, Tailwind CSS and source-owned shadcn/ui
components. This implements the user's third UI choice on 2026-10-21. It replaces
the handwritten management page while preserving the existing Go management
proxy, collector, session-management API and CLI. The paired plugin, collector
and CLI were deployed to the Mac mini M4 on 2026-10-21 after the user's explicit
request. See the [deployment record](mac-mini-deployment.md) for access and rollback.

The next [automatic organization and bulk-job slice](session-automation.md) adds
computed views, frozen all-matching previews and resumable apply/undo in
development. Its generated build is not installed by the earlier rollout.

## Source and packaging

- `frontend/src/app.tsx` owns connection, hash routes, responsive layout, theme,
  project navigation and streaming export tickets.
- `frontend/src/components/session-library.tsx` owns literal label search, exact
  facets, tags, favorites, archive visibility, pagination and loading/error states.
- `session-editor.tsx` edits user metadata with inspected revisions. Conflicts or
  ambiguous failures retain the local draft until an explicit reload. Navigation
  confirms before discarding unsaved changes.
- `session-viewer.tsx`, `request-viewer.tsx` and `content-viewer.tsx` provide the
  session/turn/request/tool/raw drill-down. Long source text and diagnostic payloads
  remain lazy. Native client envelopes and normalized CPA payloads share typed
  display helpers in `frontend/src/lib/conversation.ts`.
- `frontend/src/components/ui/` contains official shadcn/ui registry sources, not
  an imitation of their styles. Source URLs, original source hashes and import
  adjustments are recorded in `frontend/shadcn-sources.json`; the upstream MIT
  notice is retained in `frontend/SHADCN-LICENSE.md`.

The production build is one self-contained `internal/archive/web/index.html`.
The existing `go:embed` and `/v0/resource/plugins/cpa-session-archive/index.html`
route continue to serve it. Runtime JavaScript, styles and icons are bundled;
there is no CDN or separate Node service in the production serving path. Native
Go builds consume the checked-in generated file without installing frontend
dependencies. CI rebuilds and compares it to prevent stale embedded artifacts.

Do not edit the generated HTML directly. Edit the React source, run the frontend
build and include both source and generated output in a review. Preserve hash
URLs so embedding in CPA and bookmarked conversation/tool routes continue to
work. The library remains mounted while viewing a detail route, retaining its
applied filters and page. Replacing a detail entity resets its pending requests
and state so a previous session cannot appear under another ID.

## Development commands

Use the repository's Node 24.18.0 and pnpm 11.24.0 toolchain:

```text
pnpm install --frozen-lockfile
pnpm typecheck
pnpm build:frontend
pnpm check:frontend
pnpm test:scripts
```

`pnpm dev` runs the loopback Vite development server. A live backend is never
configured automatically. For a working preview that needs no credential or
private conversation, use:

```text
pnpm preview:frontend
```

This builds and starts a loopback-only synthetic preview at
`http://127.0.0.1:18818`. Set `CPA_PREVIEW_PORT` to choose another port. The preview
uses the isolated E2E mock with a synthetic management token, marks itself as a
synthetic preview in the UI and never contacts a live CPA service. Stop the
command to stop the preview. Existing services are not restarted. This preview
is not production rollout or real-history acceptance.

Browser regression commands:

```text
pnpm exec playwright install chromium --only-shell
pnpm test:e2e
```

The test server binds `127.0.0.1:4173` by default. Set `CPA_E2E_PORT` to select an
unused port. Tests never reuse an existing server, so a stale page or unrelated
service cannot silently satisfy the gate. No real credentials are needed. Tests
exercise actual metadata saves, archives/restores, filters, pagination, lazy
views, native tool references, streamed downloads, scope-safe IDs, i18n, themes
and mobile navigation.

## Connection and data behavior

The embedded page reads the host's existing `cli-proxy-auth` envelope, including
its current encoded form. A manual connection keeps the typed credential in
page memory and does not persist it. Requests use the authenticated management
proxy with redirects disabled. Switching connections clears the old endpoint's
session cache, aliases, filters and download tickets. The separate preview flag
is injected by the synthetic mock, not enabled by a production build.

Organization metadata is rendered literally. Transport-escaped source previews
are decoded for readability and rendered as React text, never injected markup.
Raw payloads retain a separate exact decoded-text view. Export tickets stream
through an iframe or reusable link; the browser never buffers whole JSONL files.
Theme choice is local and contains no credential. Host language changes remain
observable and the page provides Chinese and English labels.

This UI adds no chat-completion route, automatic model extraction, human memory
approval, or task-completion inference. Those remain the separate stages in the
[session management plan](plans/2026-10-21-session-management.md).

## Verified development acceptance, 2026-10-21

Validation used Node 24.18.0, pnpm 11.24.0 and the pinned Playwright 1.62.1 browser.
All 15 browser scenarios, 21 script/normalization/repository checks, TypeScript
checks, deterministic embedded-build comparison, repository-wide Go race tests
and `go vet` passed. The script runner now includes root-level repository
contracts as well as nested parser tests. Its source inventory follows Git;
existing documented capacity/native-history/local-embedding adapters are kept
explicit while release/frontend tooling stays TypeScript. No operational
adapter or ignored local cache was deleted.

Native collector, CLI and C-shared plugin builds passed. The plugin was checked
to contain the exact final HTML (507,612 bytes; SHA-256
`2f16d467525fb5908fb9ccbb55a592fc32b192482b7b38dab86df0411b3c858e`).
Desktop 1280×720 and mobile 390×844 screenshots were inspected. A separate clean
browser opened the synthetic preview without an auth envelope, connected to its
mock automatically, reported no page errors, made no external requests and
persisted no credential. This is isolated synthetic acceptance, not a claim of
live CPA integration or deployment.

## Live rollout acceptance, 2026-10-21

The native plugin, collector and CLI from source commit
`baa4650f23336b9adec3d8430be03f7c1b07e4e0` were installed together. The live CPA
resource served the exact embedded page hash above; a browser rendered its
login screen without page errors. Collector readiness, session browsing and
session-scoped turn reads passed. Installed artifact hashes matched, retained
raw record counts advanced, and the producer outbox and quarantine were empty.

The independent memory service was not restarted. Its ingestion lag returned
to zero, no active error remained, and its cumulative failure counter stayed
stable during the final observation window. Three existing citations returned
the same text hashes; protected CPA, memory, LaunchAgent and Codex files stayed
unchanged. No client integration or new memory workflow was enabled.

Anonymous management requests were rejected. A positive live management login
or write was not tested because no existing plaintext management credential was
retrieved or reset. Authenticated management and native CLI operations passed
against the actual CPA executable in isolated fixtures with synthetic credentials.
