# Changelog

All notable changes to this project are recorded here. The version stays `0.1.0`:
these changes are not a release, and nothing in this file should be read as one.

The tool surface is pinned to the reference memory server by
`src/memory/testdata/reference-tools.json`; see
[README.md](README.md#compatibility-with-the-reference).

## Unreleased

### Fixed

- **A panic in a handler no longer kills the process.** The SDK runs handlers on its own
  goroutines, so `net/http`'s per-connection recovery did not apply and one request could
  take down every project. A receiving middleware now converts a panic into a JSON-RPC
  `-32603` internal error, logs the recovered value, and keeps the server serving.
- **Sessions are scoped to their project.** A session minted under project A used to be
  served A's data when driven at `/project/B`, and created B's directory on the way. A
  session ID is now `<projectId>.<random>` and a request presenting a foreign session is
  refused with `404` before any project state is opened or created.
- **Signals no longer report failure.** With a client holding the standalone SSE stream
  that a URL-only client opens, the graceful wait expired and the process exited `1` on
  every restart. Shutdown now drains for up to five seconds, closes the remaining
  connections, logs why, and exits `0`. Every acknowledged write is already durable.
- **A project can no longer be wedged by the project cap.** Reaching
  `GASSAWAY_MAX_PROJECTS` returned `429` for every new project until restart. The cap now
  bounds an LRU of project runtimes: the least recently used is dropped and rebuilt on
  demand, and its data is untouched.
- **A request for an unknown project no longer allocates storage.** Opening a project
  used to create its directory and `project.json` before the MCP handshake. Storage now
  appears with the first write; a read of a project that holds nothing leaves the data
  directory exactly as it was.
- **A failed tool call is now visible.** The injected logger was unused and SDK logging
  was discarded, so a rejected call left no server-side evidence. One `Warn` (tool error)
  or `Error` (transport or panic) record per failed call now carries `project`, `tool`,
  `error` and `duration_ms`; successes are `Debug`. Arguments, entity names and
  observation text are never logged.
- **Server errors are answered generically.** A `5xx` body used to quote internal paths;
  the detail now goes to the log and the client is told storage is unavailable.
- **`create_entities` says when it skipped a name.** A skipped entity keeps none of its
  observations, so the result now carries a second text item naming the skipped entities
  and pointing at `add_observations`.

### Changed

- **The wire contract is now the reference's, and is pinned by a test.** Argument and
  result schemas are authored artifacts transcribed from a committed capture
  (`src/memory/testdata/reference-tools.json`, `PROVENANCE.md`) instead of being inferred
  from Go types. Titles are published; `openWorldHint: false` is set on every tool;
  `idempotentHint` is `true` on the three additive tools, which dedupe.
- **Argument objects are open.** A payload with an unknown field is accepted and ignored;
  a payload missing a required field reports that field. Previously the closed object
  reported the unknown field first, which hid the actionable message.
- **Result shapes follow the reference (breaking).** `delete_entities`,
  `delete_observations` and `delete_relations` return `{success, message}` instead of
  `{deleted, notFound}`, `{deletedCount, missingEntities}` and `{deletedCount}`.
- **The read tools accept the reference's optional arguments (breaking for the published
  schema, additive for callers).** `read_graph` takes `entityTypes` and
  `observationLimit`; `search_nodes` takes `limit` and `observationLimit`; `open_nodes`
  takes `observationLimit`. Each entity carries `observationCount` only when an
  observation limit was given.
- **`GASSAWAY_LOG_LEVEL`** (`debug`, `info`, `warn`, `error`; default `info`) selects the
  log level. An unknown value refuses the start with exit code `2`.
- **Project lifecycle.** `/healthz` counts projects that hold stored data, not projects
  that have been visited. `project.json` appears with the first write.
- **`GASSAWAY_MAX_PROJECTS` semantics** changed from "refuse past the cap" to "evict the
  least recently used runtime".

### Added

- A path-scoped graph write lock, so two runtimes over one graph cannot lose an update.
- Tenant-isolation tests covering tools, the resource, session reuse and identity forms.
- Fuzz targets for project-ID validation and hashing.
- This changelog, and the design record in [DESIGN.md](DESIGN.md) /
  [IMPLEMENT.md](IMPLEMENT.md).

### Deferred

- **Origin validation.** No browser can reach the JSON endpoint (no CORS headers are
  emitted, and a simple cross-origin POST is refused), and a remote deployment will sit
  behind a proxy that can enforce the policy. Revisit at the first browser client, the
  first `Access-Control-Allow-Origin` header, or the move off loopback.
- **Authentication and TLS.** Planned for the shared deployment; the server is
  loopback-only until then.
- **The `2026-07-28` protocol era.** Needs the SDK's stateless handler, which removes
  sessions. A newer client is answered with `-32022` and the supported list so it can
  renegotiate down.
- **SQLite/FTS5 storage.** Measured at the largest known graph (45 KiB): reads under a
  millisecond. The swap would add ~10 MB to the binary and 26 modules; the graph package
  is the seam if it is ever justified.
- **Timestamps, provenance and soft delete.** No consumer today; the format preserves
  unknown fields, so they can land without a migration.
- **Resource subscriptions.** The consumer uses tools.
- **A read timeout on the HTTP server.** It risks cutting long-lived SSE streams; that
  belongs at the fronting proxy, or with the 2026 protocol's request-cancellation option.
- **Quotas and per-tenant rate limits.** There is no authenticated identity to key them
  on yet.
