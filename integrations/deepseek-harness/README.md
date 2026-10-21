# DeepSeek Harness session archive plugin

Development Cordis plugin for DeepSeek Harness. It observes provider-neutral
`llm/stream` calls and sends completed evidence to the native
`cpa-session-capture` companion. The companion shares the CPA archive's bounded
queue, SQLite outbox and commit-receipt implementation.

Build the companion from the archive checkout and set the runtime row's config:

```yaml
name: '@aias/dsh-session-archive'
config:
  binary: /absolute/path/to/cpa-session-capture
  endpoint: http://127.0.0.1:8318
  spoolDir: /absolute/private/deepseek-harness-outbox
```

The bundled row is disabled until configured. The companion binary is not part
of this npm package. No model request is modified and no memory is injected.
Preserve the private outbox directory across plugin reloads. Capture before
outbox persistence can be lost after a crash or capacity rejection. Local
attachment references are retained as references, not copied from disk.

From the repository root, `pnpm build:plugins` builds JavaScript/declarations;
`pnpm verify:host-plugins` verifies real host-source contracts when
`MAGPIE_SOURCE`, `DSH_SOURCE` and `GO_BINARY` are supplied. See
[full contracts and limitations](https://github.com/Aias00/cpa-session-archive/blob/main/docs/host-plugins.md).
