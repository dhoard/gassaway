# Design Plan — Tenant-safe sessions, crash containment, shutdown, a bounded project cache, and a pinned wire contract

**Status:** Implemented (see [IMPLEMENT.md](IMPLEMENT.md) §9 for the execution record and evidence)
**Request:** User instruction, 2026-11-06, following an audit of this server against the reference memory server, the official Go SDK, mark3labs/mcp-go, Graphiti, mem0, basic-memory and the MCP specification
**Scope:** `src/server` (panic recovery, session binding, shutdown, logging, LRU + lazy project resolution), `src/memory` (authored schemas, titles, annotations, result shapes, read options, fixtures), `src/graph` (path-scoped write lock, prepare hook, read options), `src/config` (log level), `src/e2e`, `src/projects` (fuzz), `README.md`, `CHANGELOG.md`, this record
**Not in scope:** authentication, TLS, Origin/CORS validation, the `2026-07-28` protocol era, storage replacement, timestamps/provenance, resource subscriptions, quotas
**User constraints honored:** no authentication (the client sends a bare URL); no Origin validation (the service will later run on a shared remote host); **breaking changes are allowed — no backward compatibility, no migration code, no adapter code**; **the version stays `0.1.0`** (explicit instruction, overriding the earlier plan to bump it); the running unit, its port and its data directory are not to be touched.
**Related:** `PROMPT_ENGINEER.md` (authoring standard). No `AGENTS.md` or `PROJECT.md` exists; the project gate is `build.sh` and the README's Development section.
**Supersedes:** the earlier logging-and-wire-contract proposal in this file. Its premise that the reference publishes no output schema was false (§2 E-07), and its "keep the deviations" decisions were withdrawn once breaking changes were authorized.

## 1. Problem, goal, and observable outcome

**Problem.** The audit found six defects that matter more than any missing feature, plus a wire contract that was an artifact of Go type inference:

1. A panic in any handler terminated the process for **every** tenant: the SDK runs handlers on its own goroutines, so `net/http`'s per-connection recovery does not apply, and the SDK has no `recover()`.
2. A session created under project A was served **A's data** when used against `/project/B`, and created B's directory in the process.
3. `Shutdown` could not complete while a client held the standalone SSE stream a URL-only client opens: it returned `context deadline exceeded` and the process exited `1` on every restart.
4. Any valid 64-hex path segment created a project directory, and the project cap, once reached, never freed a slot — an unauthenticated caller could wedge the service.
5. No log record was produced for a failed call, although a logger was constructed: `ServerOptions.Logger` was never set, so SDK logging was discarded.
6. The published `tools/list` disagreed with the reference on titles, every annotation hint, schema openness, the read tools' optional arguments and the `delete_*` result shapes.

**Goal.** A process that cannot be killed by one request, cannot serve one tenant's data under another tenant's URL, exits cleanly on SIGTERM, cannot be wedged by hash-spamming, publishes exactly the reference's tool contract, and leaves one log record per failed call.

**Success looks like:** a panicking tool returns a JSON-RPC error and the process keeps serving; `POST /project/B` with A's session returns `404` and creates nothing; SIGTERM with a connected SDK client exits `0`; a read of an unknown project writes nothing; 1,200 distinct projects keep working; the nine published argument and result schemas and all annotations equal the committed capture; one failed call yields one log record with project, tool and reason.

**Must remain unchanged:** the JSONL record format and its durability invariants; project identity; the two URLs; exit code `2` for usage errors; `/healthz`'s payload shape; **no authentication; no Origin validation; version `0.1.0`**.

## 2. Current state and evidence

| ID | Evidence type | Source / command | Finding | State |
| --- | --- | --- | --- | --- |
| E-01 | Probe | panicking tool over an in-memory transport | `panic: assignment to entry in nil map` escaped on the SDK's goroutine and killed the test binary | Observed |
| E-02 | Source | `go-sdk@v1.8.0/mcp`: no `recover()` outside tests | The SDK converts no panic into an error | Observed |
| E-03 | Probe | session from `/project/A`, `POST /project/B` with `Mcp-Session-Id` | 200 with A's entity; B's directory created | Observed |
| E-04 | Probe | `Shutdown(ctx)` with a client holding the standalone SSE stream | `context deadline exceeded` after the full timeout | Observed |
| E-05 | Source | `src/cmd/gassaway/main.go` shutdown path | A shutdown error returned exit code 1 | Observed |
| E-06 | Probe | `POST` with `Origin: http://evil.example` | 200: no Origin validation | Observed |
| E-06b | Probe | CORS preflight, and a `text/plain` cross-origin POST | `405` with no `Access-Control-Allow-Origin`; `415` | Observed |
| E-07 | Capture | `npx -y @modelcontextprotocol/server-memory` 2.1.0 (node 26.7.0, negotiated `2025-11-25`) | The reference publishes `outputSchema`, `title`, `openWorldHint:false`, `idempotentHint:true` on additive tools, and **open** argument objects | Observed |
| E-08 | Capture | same capture vs. this server's live `tools/list` | Titles absent; `idempotentHint:false` on `create_*`/`add_observations`; `openWorldHint` absent; closed argument objects; `delete_*` result shapes differ; read tools lack the optional arguments | Observed |
| E-09 | Source | `grep -rn 's\.log'` — no match; no `Logger` in `ServerOptions` | The logger was unused and SDK logging discarded | Observed |
| E-10 | Source | `src/server/registry.go` | Projects were memoized forever; the cap returned 429 permanently | Observed |
| E-11 | Source | `src/server/registry.go` `Get` | Directory and `project.json` were created before the SDK saw the request | Observed |
| E-12 | Source | `src/graph/store.go` | The mutex was per `Store`; two `Store`s over one path would not exclude each other | Observed |
| E-13 | Measurement | synthetic graphs through `graph.Store` | read 0.69 ms / 7.0 ms / 37.7 ms; mutation 0.95 ms / 9.5 ms / 51 ms at 28 KiB / 313 KiB / 2.2 MiB | Observed |
| E-14 | Baseline | `go build ./...`, `go vet ./...`, `gofmt -l ./src`, `go test ./...`, `go test -race ./...` | Green before any change | Observed |
| E-15 | Dependency probe | `modernc.org/sqlite` + FTS5 | Feasible without cgo, but +10.0 MB binary and 26 modules | Observed |
| E-16 | User instruction | 2026-11-06 | Breaking changes allowed; no backward compatibility, migration or adapter code; version stays `0.1.0` | Observed |

## 3. Requirements, and acceptance criteria

| ID | Requirement | Observable acceptance criterion | Priority |
| --- | --- | --- | --- |
| R-01 | A panic in a tool, resource or protocol handler becomes a JSON-RPC internal error and the process survives | Panicking tool returns an error, the next call succeeds, the process is alive, the recovered value is not disclosed | P0 |
| R-02 | A session minted under one project is refused under another project's URL before any state is opened or created | `POST`/`GET`/`DELETE` at B with A's session ⇒ `404`; B's directory does not exist; A's graph unchanged | P0 |
| R-03 | SIGTERM drains in-flight requests up to the grace period, then closes remaining connections and exits `0` | With a connected standalone-SSE client, `Shutdown` returns nil within the grace and `Serve` returns nil; the process exits 0 | P0 |
| R-04 | Titles, annotations and the argument and result schemas of all nine tools equal the reference capture | Contract test over live `tools/list` against `testdata/reference-tools.json`, structurally, both directions | P1 |
| R-05 | Argument objects are open; unknown fields are ignored; a missing required property is reported as such | Acceptance corpus: unknown envelope field accepted; flattened payload reports the missing property | P1 |
| R-06 | `create_entities` names the entities it skipped in the result text | Second text item present when something was skipped, absent otherwise | P1 |
| R-07 | Every failed `tools/call` emits exactly one `Warn`/`Error` record with `project`, `tool`, `error`, `duration_ms`; successes are `Debug`; HTTP refusals are `Warn`; graph content never appears | Captured-handler tests; e2e assertion against captured stderr; sentinel observation absent from every record at every level | P1 |
| R-08 | `GASSAWAY_LOG_LEVEL` is validated at start-up | `config` unit test; junk value exits `2` naming the variable | P1 |
| R-09 | A request for a project with no stored data creates nothing; the first write publishes the directory and its metadata atomically | Router test: root empty after a read, complete after a write | P2 |
| R-10 | The project cap bounds open runtimes by LRU instead of refusing; one graph path has one write lock per process | 1,200 distinct projects all serve; two stores over one path prove serialization; a session on a dropped runtime keeps working | P2 |
| R-11 | Tenant isolation is proven for every surface, and identity parsing is fuzzed | Isolation table over tools, resource, sessions and identity forms; fuzz targets | P2 |
| R-12 | No authentication, no TLS, no Origin validation — documented with revisit conditions | `README.md` "Security posture" | P2 |
| R-13 | Stored format, project identity, URLs, exit code `2` and `/healthz` payload unchanged; version stays `0.1.0` | Existing suites; `git diff` limited to the inventory | P0–P2 |

## 4. Proposed design and rationale

**Panic containment (R-01).** Each per-project `*mcp.Server` gets `recoverPanics`, a receiving middleware installed in `Registry.Get` before the tools. It recovers, logs the value with the project and method, and returns `&jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "internal error"}`. A middleware is used because the panic runs on the SDK's goroutine; the same wrapper covers resource reads and the SDK's own argument paths. The recovered value is not returned to the client: it can quote graph content.

**Session binding (R-02).** `ServerOptions.GetSessionID` is per server and the registry builds one server per project, so session IDs become `<projectID>.<crypto/rand.Text()>`. `withProject` checks the prefix before resolving or opening the project and answers `404` — the status the protocol assigns to an unknown session — on a mismatch. No side table, one enforcement point, and the random suffix stays the credential. Sessions are process-local, so an upgrade cannot strand an unprefixed session.

**Shutdown (R-03).** `Shutdown` keeps the bounded graceful wait; when it expires the server is closed, the reason is logged at `Warn`, and `Shutdown` returns nil so the process exits `0`. The SDK exposes no way to close sessions (its `closeAll` is test-only) and a standalone SSE stream never goes idle, so forced close is the only lever. It is safe because every acknowledged write is already durable.

**Wire contract (R-04, R-05, R-06).** Both argument and result schemas are authored JSON literals in `src/memory/schemas.go`, transcribed from the committed capture, and passed as `Tool.InputSchema`/`Tool.OutputSchema`; typed handlers still decode and validate. Result schemas must be authored because Go inference emits `["null","array"]` for a slice while the handlers always send an array — a client validating results against the published schema has to be told what is actually sent. `create_entities` adds the skipped-entity notice; the three delete tools return `{success, message}`. `schemas.go` literals are generated from the capture once and reviewed as source; the test compares the live wire contract against the capture structurally (property names, types, `required`, openness), excluding prose.

**Observability (R-07, R-08).** `ServerOptions.Logger` is wired; `toolCallLogger` times each `tools/call` and classifies the outcome (`GetError()` for a tool error, `err` for a protocol error). The logger is outermost and recovery inside it, so exactly one record is produced per failure and a panic is recorded by the recovery wrapper. Field set is fixed: `project`, `tool`, `error`, `duration_ms`; HTTP refusals log method, path, status and reason. Arguments, entity names and observation text are never logged. A `5xx` body becomes generic, with the detail in the log.

**Project lifecycle (R-09, R-10).** `Registry.Get` no longer touches the filesystem: it validates the ID, reuses a memoized runtime, and only *validates* `project.json` when the directory exists. The directory and metadata are published by the first save through `graph.Store.SetPrepare`. The runtime map is an LRU bounded by `GASSAWAY_MAX_PROJECTS`; eviction drops the runtime and the next request rebuilds it. Because a graph path's write lock is owned by the `graph` package keyed by resolved path, two `Store`s over one path (before and after eviction) still exclude each other — the invariant that makes eviction safe.

**Rejected alternatives.** *Recovery at the HTTP edge* — misses the goroutine the panic runs on. *A session→project side table* — a second source of truth to keep consistent. *Waiting for SSE clients* — unbounded, with no hook to end a stream. *Keeping the old result shapes and documenting deviations* — withdrawn when the user authorized breaking changes; parity is worth more than a documented difference. *Replacing the store* — measured cost at the largest known graph is under a millisecond; +10 MB and 26 modules. *Origin validation now* — no browser can reach the JSON endpoint, and it would have to be reconfigured for the shared deployment.

## 5. Edge cases and failure behavior

| Scenario | Required result | Forbidden side effect | Req |
| --- | --- | --- | --- |
| Panic in a tool or resource handler | `-32603`, generic message | Process exit; panic value disclosed | R-01 |
| Foreign session at another project (POST/GET/DELETE) | `404` before any project work | B's directory; A's data returned | R-02 |
| Forged session suffix | `404` from the SDK's session table | Acceptance | R-02 |
| SIGTERM with an SSE client | Drain, close, exit `0` | Hang; exit `1` | R-03 |
| SIGTERM with no clients | Drain, exit `0` | Forced close logged as a warning | R-03 |
| Read of a project with no data | Empty graph | Any file or directory created | R-09 |
| First write | Directory + `project.json` + graph | Partial metadata | R-09 |
| Corrupt `project.json` | Open fails; generic 500 to the client, detail logged | Silent replacement; path in the body | R-13 |
| 1,200 distinct projects | All serve; least recently used runtime evicted | 429 wall; lost writes after eviction | R-10 |
| Session on an evicted runtime | Keeps working | Lost or duplicated writes | R-10 |
| Unknown envelope field | Accepted and ignored | "additional properties" rejection | R-05 |
| Flattened payload | Required-property error | Silent acceptance | R-05 |
| `observationLimit: 0` | Names, types and counts only | Observations returned | R-04 |
| Failed call | One log record, no arguments or content | Content in the log | R-07 |
| Junk `GASSAWAY_LOG_LEVEL` | Exit `2` naming the variable | Silent fallback | R-08 |

## 6. Compatibility, migration, and operational impact

**Compatibility is not a constraint for this change** (user instruction). The published tool contract is now the reference's, which replaces this server's earlier shapes: `delete_*` return `{success, message}`; the read tools accept `entityTypes`/`limit`/`observationLimit` and report `observationCount` only when limited; titles and `openWorldHint` appear; argument objects are open. `README.md` §"Compatibility with the reference" and `CHANGELOG.md` record this. No adapter or compatibility layer exists, by instruction and by design.
**Stored data:** the JSONL format, `schemaVersion: 1`, record shapes and directory layout are unchanged, so existing graphs are read and written as they are. The only lifecycle change is *when* an empty project's storage appears: its first write instead of its first request. Nothing is migrated.
**Version:** stays `0.1.0` (user instruction); `/healthz` reports the same value as before.
**Operations:** `GASSAWAY_MAX_PROJECTS` now bounds a cache; `GASSAWAY_LOG_LEVEL` is new; `/healthz` counts projects with stored data. A signal drains for up to five seconds and exits `0`. Rollback is a binary swap: no persistent artifact changes shape.
**Security posture:** no authentication, no TLS, no Origin validation; loopback bind, the SDK's loopback `Host` check, no CORS headers, and project-scoped sessions are what remain. Revisit conditions are recorded in the README.

## 7. Validation strategy

| Requirement | Proof | Negative / preservation check |
| --- | --- | --- |
| R-01 | `src/server/recovery_test.go`: panicking tool and resource over an in-memory transport | Second call succeeds; panic value absent from the client's error |
| R-02 | `src/server/isolation_test.go`: cross-project POST/GET/DELETE | No directory created; A's graph unchanged |
| R-03 | `src/server` shutdown test with a default (SSE-enabled) client; `src/e2e` SIGTERM case | `Serve` returns nil; exit code 0 |
| R-04 | `src/memory/surface_test.go` against `testdata/reference-tools.json` | Fails if a schema, title or annotation drifts in either direction |
| R-05 | Acceptance corpus in the same file | Unknown field accepted; missing-required reported; no closed argument object |
| R-06 | `TestSkippedNoticeNamesTheEntities` | No notice when nothing was skipped |
| R-07, R-08 | `src/server/logging_test.go`, `src/e2e` stderr assertion, `src/config` tests | Sentinel absent; no `Warn`/`Error` for success; junk level exits 2 |
| R-09 | `src/server/router_test.go` over an empty temp root | `project.json` absent after a read, present after a write |
| R-10 | LRU router test; `src/graph` two-store serialization test | No lost updates under `-race`; dropped-runtime session still works |
| R-11 | Isolation table; `src/projects/fuzz_test.go` (8 s each, ~1.8 M execs) | Refused ID never yields a path |
| R-12 | `README.md` inspection | — |
| R-13 | The pre-existing suites | Diff limited to the inventory; version unchanged |

Gate: `gofmt -l ./src` empty, `go vet ./...`, `go test -race ./...`, and a `CGO_ENABLED=0` build to a temp path. `staticcheck` is not installed on this host (recorded, not silently skipped). `build.sh` itself was not run because it writes `bin/gassaway`; its steps were run individually so the repository's build artifact is left alone while the deployed service runs.

## 8. Risks and assumptions

| Item | Impact | Mitigation / outcome |
| --- | --- | --- |
| Session ID reveals the project hash | A client that already knows the hash sees it echoed | Accepted: the random suffix is the credential, and the hash is not a secret from the client that supplies it |
| Forced close cuts an in-flight stream | A client sees a closed stream on shutdown | Every acknowledged write is durable; the reason is logged |
| Eviction rebuilds a runtime while a session holds the old one | Extra allocation; two stores over one path | Shared path lock; store test proves serialization |
| Lazy creation changes when `project.json` appears | Operators see a directory only after a first write | Documented; covered by R-09 |
| Authored schemas could drift from the capture | The model would see a contract the server does not implement | Both schemas are generated from the capture and the test compares the live wire form against it |
| Breaking result shapes break an old client | An old consumer misreads a result | Authorized; the single consumer is updated with it, and the change is recorded in `CHANGELOG.md` |
| `go test -race` and the 5 s shutdown grace slow the gate | ~7 s added to e2e | Accepted |

## 9. Decision and handoff

**Decision status:** Implemented.
**Selected approach:** R-01–R-13. Decisions: D-01 receiving-middleware recovery; D-02 session-ID namespacing; D-03 bounded shutdown with forced close; D-04 full result-shape parity with the reference; D-05 authored argument **and** result schemas; D-06 capture-pinned contract test comparing structure both ways; D-07 fixed log field set with content excluded; D-08 lazy creation via a store prepare hook; D-09 path-scoped write lock plus LRU runtime cache; D-10 no Origin validation, documented with revisit conditions; D-11 version stays `0.1.0`.
**Deferred, with reasons:** Origin/CORS (user decision; revisit at the first browser client, the first CORS header, or the move off loopback); authentication and TLS (planned for the shared deployment); the `2026-07-28` era (needs the stateless handler, which removes sessions; newer clients renegotiate down after `-32022`); SQLite/FTS5 (+10 MB, 26 modules, measured cost at 45 KiB under a millisecond); timestamps/provenance/soft delete (no consumer; the format tolerates them as extras); resource subscriptions (consumer uses tools); quotas and rate limits (no authenticated identity); `ReadTimeout` (risks cutting SSE streams; belongs at the fronting proxy).
**Next artifact:** [IMPLEMENT.md](IMPLEMENT.md) §9 — the execution record.

> **Handoff boundary:** Implementation is complete and verified locally. Deployment is not authorized: `install.sh` writes `/opt`, `/etc/systemd/system` and `/var/lib`, and the running unit must not be patched or interrupted.
