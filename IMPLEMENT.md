# Implementation Plan — Tenant-safe sessions, crash containment, shutdown, a bounded project cache, and a pinned wire contract

**Status:** Executed (see §9)
**Request:** User instruction, 2026-11-06 — create and implement a plan to improve the project, using files for the plan
**Design:** [DESIGN.md](DESIGN.md)
**Implementation authorization:** Granted by the same instruction. Deployment is **not** authorized: the running unit, `/var/lib/gassaway` and port 4029 were not touched.
**Constraints:** no authentication; no Origin validation; breaking changes allowed, so no backward compatibility, migration or adapter code; version stays `0.1.0`; no new module in `go.mod`; JSONL format, project identity and the two URLs unchanged.
**Related:** `PROMPT_ENGINEER.md` (authoring standard), `README.md` (operator truth), `CHANGELOG.md` (what changed for a consumer)

## 1. Design contract and readiness

**Baseline:** `e1034ea` plus the uncommitted `DESIGN.md`/`IMPLEMENT.md` pair that this increment replaced. Tree otherwise clean; no commit was created (the user did not ask for one).

**Target:** a panic cannot kill the process; a session cannot cross a project boundary; SIGTERM exits 0 with clients attached; a probe request creates nothing on disk; the project cap cannot wedge the service; every failed call leaves exactly one log record; the published tool contract is the reference's, pinned by a test.

**Out of scope:** see DESIGN.md §9 "Deferred".

## 2. Verified starting state and change inventory

Baseline before any edit: `go build ./...`, `go vet ./...`, `gofmt -l ./src` and `go test -race ./...` were green (DESIGN.md E-14). Toolchain: `go1.26.0 linux/amd64`; `staticcheck` not installed.

| Component / path | Change | Requirement |
| --- | --- | --- |
| `src/graph/store.go` | path-scoped write lock (`lockFor`), `SetPrepare` hook, `ReadGraph`/`SearchNodes`/`OpenNodes` take `ReadOptions` | R-09, R-10, R-04 |
| `src/graph/graph.go` | `ReadEntity`, `ReadGraph`, `ReadOptions`, filtering and limiting helpers; `cloneEntity` returns a non-nil slice | R-04 |
| `src/memory/schemas.go` (new) | nine argument schemas and nine result schemas, transcribed from the capture | R-04, R-05 |
| `src/memory/testdata/reference-tools.json`, `PROVENANCE.md` (new) | committed capture of the reference `tools/list` + `resources/list` | R-04 |
| `src/memory/tools.go` | authored schemas, titles, annotation hints, read options, `{success, message}` delete results, skipped-entity notice | R-04–R-06 |
| `src/memory/resource.go` | title and description matching the reference | R-04 |
| `src/memory/surface_test.go` (new) | contract test (both directions, inputs and results), acceptance corpus, read-option tests, delete-shape tests | R-04–R-06 |
| `src/server/recovery.go` (new) | receiving middleware converting a panic into `-32603` | R-01 |
| `src/server/logging.go` (new) | `tools/call` outcome middleware | R-07 |
| `src/server/registry.go` | logger, `GetSessionID` prefix, LRU, lazy open, prepare hook, both middlewares | R-01, R-02, R-09, R-10 |
| `src/server/router.go` | session-prefix check, edge logging, generic 5xx bodies, drain-then-close shutdown | R-02, R-03, R-07 |
| `src/server/recovery_test.go`, `isolation_test.go`, `logging_test.go` (new) | R-01, R-02, R-07 | — |
| `src/server/router_test.go` | updated for lazy creation, LRU, metadata timing, shutdown | R-03, R-09, R-10 |
| `src/config/config.go` | `ParseLogLevel`, `EnvLogLevel`, `Config.LogLevel` | R-08 |
| `src/e2e/e2e_test.go` | SIGTERM-with-client case, failed-call log assertion, junk log level, isolation storage assertions | R-03, R-07, R-08 |
| `src/projects/fuzz_test.go` (new) | fuzz `Validate` and `ID` | R-11 |
| `README.md`, `CHANGELOG.md` (new) | surface, configuration, logging, compatibility, security posture | R-12 |
| `src/buildinfo/version.go` | **not changed** — version stays `0.1.0` (user instruction) | R-13 |

## 3. Executed steps

| Step | Change | Result |
| --- | --- | --- |
| I-01 | `graph`: path-scoped lock + prepare hook + tests | Two stores over one path: 25 of 50 writes were lost with independent mutexes (verified by temporarily reverting `lockFor`), none lost with the shared lock |
| I-02 | `memory`: capture, schema literals, titles, annotations, result shapes, read options, skipped notice | Contract test fails on a closed object or a renamed property (verified by temporarily editing `schemas.go`) |
| I-03 | `server`: panic recovery | Without the middleware the test binary dies with `panic: assignment to entry in nil map`; with it, the call returns an error and the next call succeeds |
| I-04 | `server`: session-ID prefix + edge check | Cross-project POST/GET/DELETE ⇒ 404, nothing created; a forged suffix ⇒ 404 |
| I-05 | `server`: shutdown drain + forced close | `Shutdown` returns nil in ~250 ms with an SSE client (grace-exhausted close), `Serve` returns nil, the acknowledged write is on disk |
| I-06 | `server` + `config`: logging, log level, generic 5xx | One `Warn` per failed call (including argument-validation failures), none for a success, sentinel observation absent at `debug` and `info` |
| I-07 | `server`: lazy resolution + LRU | A read of an unknown project leaves the root empty; one project's `project.json` appears on first write; a cap of 1 serves two projects with eviction, and the evicted runtime's session keeps working |
| I-08 | `projects`: fuzz targets | `FuzzValidate` 1.83 M execs / 9 s, `FuzzID` 338 k execs / 6 s, both clean |
| I-09 | Docs: README, CHANGELOG, DESIGN, IMPLEMENT | Version left at `0.1.0` |
| I-10 | Full gate | See §9 |

## 4. Requirements-to-changes traceability

| Requirement | Steps | Verification |
| --- | --- | --- |
| R-01 | I-03 | `TestPanicInToolHandlerIsContained`, `TestPanicInResourceHandlerIsContained`, `TestRecoveredPanicIsNotDisclosed` |
| R-02 | I-04 | `TestForeignSessionIsRefusedForEveryMethod`, `TestSessionsAreScopedToTheirProject` |
| R-03 | I-05 | `TestShutdownSucceedsWithAConnectedClient`, `TestSigtermExitsCleanlyWithAConnectedClient` |
| R-04, R-05, R-06 | I-02 | `TestInputSchemasAreTheReferenceContract`, `TestToolMetadataMatchesTheReference`, `TestResourceMatchesTheReference`, `TestAcceptedPayloads`, `TestReadToolOptions`, `TestDeleteToolsReportLikeTheReference`, `TestSkippedNoticeNamesTheEntities` |
| R-07, R-08 | I-06 | `TestFailedToolCallIsOneWarnRecord`, `TestInvalidArgumentsAreLogged`, `TestSuccessfulCallsAreNotWarned`, `TestLogsCarryNoGraphContent`, `TestEdgeRefusalsAreLogged`, `TestServerErrorBodyIsGeneric`, `TestFailedCallIsVisibleInTheServersLog`, `TestJunkLogLevelIsRefused` |
| R-09, R-10 | I-01, I-07 | `TestUnknownProjectIsNotCreatedByReading`, `TestHealthCountsStoredProjects`, `TestProjectCapEvictsTheLeastRecentlyUsed`, `TestTwoStoresOnOnePathSerialize`, `TestPrepareRunsOnceBeforeTheFirstSave` |
| R-11 | I-04, I-07, I-08 | `TestProjectCannotReachAnotherProjectsData`, `FuzzValidate`, `FuzzID` |
| R-12 | I-09 | `README.md` "Security posture" |
| R-13 | all | The pre-existing suites pass; version unchanged |

## 5. Regression and edge-case matrix (as verified)

| Scenario | Outcome | Test |
| --- | --- | --- |
| Panic in a tool | `-32603`, process alive | `recovery_test.go` |
| Panic in a resource | `-32603`, process alive | `recovery_test.go` |
| Panic value quoted from graph content | Not disclosed | `TestRecoveredPanicIsNotDisclosed` |
| Foreign session, POST/GET/DELETE | 404, nothing created | `isolation_test.go` |
| Forged session suffix | 404 | `TestSessionsAreScopedToTheirProject` |
| SIGTERM with SSE client | exit 0, write durable | `e2e` `TestSigtermExitsCleanlyWithAConnectedClient` |
| Read of an unknown project | empty graph, root untouched | `TestUnknownProjectIsNotCreatedByReading` |
| First write | directory + metadata + graph | `TestPathIDIsUsedUnchanged`, `TestPrepareRunsOnceBeforeTheFirstSave` |
| Corrupt metadata | open fails, generic 500, detail logged | `TestServerErrorBodyIsGeneric`, `TestMetadataSurvivesRestartAndRejectsCorruption` |
| Cap of 1, two projects | both serve, eviction logged | `TestProjectCapEvictsTheLeastRecentlyUsed` |
| Evicted runtime's session | keeps working, writes serialized | same |
| Unknown envelope field | accepted | `TestAcceptedPayloads` |
| Flattened payload | required-property error | `TestAcceptedPayloads` |
| `observationLimit: 0`, `limit`, `entityTypes` | honoured; `observationCount` only when limited | `TestReadToolOptions` |
| Failed call | one record, sentinel absent | `logging_test.go` |
| Junk log level | exit 2 | `TestJunkLogLevelIsRefused` |

## 6. Verification commands (executed)

| Order | Command | Result |
| --- | --- | --- |
| 1 | `go test ./src/graph ./src/memory ./src/config ./src/projects` | ok |
| 2 | `go test ./src/server` | ok |
| 3 | `go test ./src/e2e` | ok (~6 s, of which 5 s is the shutdown grace with a live SSE client) |
| 4 | `gofmt -l ./src` | empty |
| 5 | `go vet ./...` | clean |
| 6 | `go test -race ./...` | ok (see §9) |
| 7 | `CGO_ENABLED=0 go build -trimpath -o /tmp/gassaway-gate ./src/cmd/gassaway` | built, 12.8 MB |
| 8 | `git diff --check` | clean |
| 9 | `go test ./src/projects -run XXX -fuzz FuzzValidate -fuzztime 8s` | 1.83 M execs, clean |
| 10 | `go test ./src/projects -run XXX -fuzz FuzzID -fuzztime 5s` | 338 k execs, clean |

`build.sh` was deliberately not run: it writes `bin/gassaway`, and the deployed service is running. Its steps were run individually, with the static build going to `/tmp`.

## 7. Execution safety, integration, and rollback

- No test touched `/var/lib/gassaway`, port 4029 or the running unit; every test uses `t.TempDir()` and an ephemeral port.
- No `install.sh`, no systemd, no restart; `bin/gassaway` was not overwritten.
- Rollback: revert the source. No stored state changed shape, so no data rollback exists or is needed.
- No new module: schemas are JSON literals, the middlewares use the pinned SDK, the Origin question was closed by decision.

## 8. Risks encountered

| Risk | Outcome |
| --- | --- |
| Provided `InputSchema` rejected by the SDK's resolver | Accepted; `map[string]any` literals resolve, and the typed handlers still validate and decode |
| Authored result schemas reject the handler's own output | Happened once: `cloneEntity` turned an empty observation slice into `null`, which the result schema rejects. Fixed at the source (`cloneEntity` and `entityView` now always produce a non-nil slice) — the schema caught a real contract bug |
| `calledTool` type assertion missed the tool name | Happened: the receive side hands middleware `*mcp.CallToolParamsRaw`. Both types are now handled |
| Existing tests encoded the old behaviour | Updated where DESIGN.md §6 states the change: cap (429 → eviction), `project.json` timing, `delete_*` result shapes, `/healthz` count |
| A 5 s shutdown grace slows e2e | Accepted: it is the documented drain window |

## 9. Execution record and handoff

**Execution status:** Complete; verified locally; not deployed.
**Implementation authorization evidence:** User instruction 2026-11-06 ("use files to create and implement a plan to improve the project based on the previously provided recommendations"), with later constraints: no authentication, no Origin validation, breaking changes allowed, version stays `0.1.0`, running service untouched.
**Change tracking:** No commit created (not requested). Working tree carries the changes; `git status --short` lists exactly the paths in §2.
**Deferred:** see DESIGN.md §9 and `CHANGELOG.md` "Deferred".

### Commands actually run and observed results

| Command | Actual result |
| --- | --- |
| `go build ./...` (baseline and after each step) | clean |
| `go vet ./...` | clean |
| `gofmt -l ./src` | empty |
| `go test ./...` (baseline) | all packages ok |
| `go test -race ./...` (final) | `config 1.0 s`, `e2e 6.6 s`, `graph 1.1 s`, `memory 1.3 s`, `projects 1.0 s`, `server 1.9 s` — all ok |
| `CGO_ENABLED=0 go build -trimpath -o /tmp/gassaway-gate ./src/cmd/gassaway` | built, 12 751 265 bytes |
| `git diff --check` | clean |
| `go test ./src/projects -run XXX -fuzz FuzzValidate -fuzztime 8s` | 1 834 047 execs, PASS |
| `go test ./src/projects -run XXX -fuzz FuzzID -fuzztime 5s` | 338 320 execs, PASS |
| Mutation check: revert `graph.lockFor` to a per-store mutex | `TestTwoStoresOnOnePathSerialize` fails with 25 of 50 entities present |
| Mutation check: disable the recovery middleware | `TestPanicInToolHandlerIsContained` dies with `panic: assignment to entry in nil map` |
| Mutation check: close an argument object in `schemas.go` | `TestInputSchemasAreTheReferenceContract` fails on openness |
| `/tmp` probe copy of the tree (pre-change) | reproduced E-01, E-03, E-04, E-06; captured the reference `tools/list` |

### Final acceptance checklist

- [x] R-01–R-13 implemented, each with the test named in §4.
- [x] Negative cases covered: foreign session, forged suffix, panic disclosure, closed argument object, unknown field, flattened payload, corrupt metadata, cap exhaustion, junk log level.
- [x] Consumers updated: `README.md`, `CHANGELOG.md`, and the tool contract pinned to the capture.
- [x] Gates passed: `gofmt`, `go vet`, `go test -race ./...`, static build. `staticcheck` unavailable on this host.
- [x] No stubs, no debug code, no unrelated changes; version unchanged.
- [x] Diff reviewed against the §2 inventory; no user edits or external state were touched.
- [x] Unverified items disclosed: `staticcheck` not run; `build.sh` not run as-is (equivalent steps executed); deployment not attempted.
