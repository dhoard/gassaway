# gassaway

A memory server for AI harnesses: one long-lived process that serves the memory
knowledge graph of **many projects** over Streamable HTTP, with strict isolation
between them.

It exists so a harness like [Amanda](../amanda) can keep one project's memory in
one graph, another project's in another, from a single service started once —
instead of one server process, one container and one file per client invocation.
The project is chosen by the URL, so one service started once serves them all.

One static binary, no container and no runtime dependency — `bin/gassaway`. The
surface is two URLs: `/project/<projectId>` for that project's memory over
Streamable HTTP, and `/healthz` for the service status, its version and how many
projects it stores.

The tool surface is the reference memory server's: `create_entities`,
`create_relations`, `add_observations`, `delete_entities`, `delete_observations`,
`delete_relations`, `read_graph`, `search_nodes`, `open_nodes`, plus the
`memory://knowledge-graph` resource. The tool names, titles, annotations and the
argument and result schemas of all nine tools are pinned by a test against a
committed capture of that server (see [Compatibility with the reference](#compatibility-with-the-reference)).
The on-disk format is the same JSONL, so an existing `memory.jsonl` reads as it is.

## Build and run

```sh
./build.sh            # gofmt, go vet, go test -race ./..., then the binary into bin/
```

`./build.sh --no-tests` skips only the test run.

The server takes its address and its storage from the command line and from nowhere
else:

```sh
bin/gassaway --host 127.0.0.1 --port 4029 --project ~/.local/share/gassaway/projects
```

`--host`, `--port` and `--project` are required and have no defaults; the data directory
is created, parents included, if it does not exist, before the listener opens. Every
option is written with two leading dashes, and the single-dash spelling is refused. See
[Configuration](#configuration). Stop the server with Ctrl-C; it shuts down cleanly on
SIGINT/SIGTERM.

Because there is no container, process supervision is yours. The included installer
puts the program, the unit and the state in three separate places and starts it as a
system service; see [Install as a system service](#install-as-a-system-service).

## Install as a system service

`install.sh` installs gassaway on a systemd host as a system service, with three parts
whose lifecycles are kept apart:

| | Path | Owner |
| --- | --- | --- |
| program | `/opt/gassaway` (`bin/`, `VERSION`, `MANIFEST`, docs) | `root:root 0755` |
| unit | `/etc/systemd/system/gassaway.service` | `root:root 0644` |
| data | `/var/lib/gassaway` | `gassaway:gassaway 0750` |

The prefix holds no state, so replacing it is a plain move and removing it cannot
destroy a graph. The unit holds the deployment facts — loopback address, port, data
directory — so nothing depends on a working directory.

```sh
./build.sh                 # build and test as yourself
sudo ./install.sh          # install, enable and start on 127.0.0.1:4029
sudo ./install.sh --help   # prefix, bin directory, port, account, --no-service, --dry-run
```

Run it from the repository, as root. It never builds, and it refuses to start without
`./bin/gassaway`. It also refuses when the unit already exists, so a version change is
always two commands:

```sh
sudo ./uninstall.sh && sudo ./install.sh
```

`uninstall.sh` stops and disables the service, removes the unit, the `/usr/local/bin`
symlink and exactly the files the install recorded in `MANIFEST`, then stops.
It keeps `/var/lib/gassaway` and the `gassaway` account that owns it, because deleting
the account would orphan the data. `--purge` removes the data too, but only when the
service is stopped and the directory holds nothing but project directories. Pass the
same `--bin-dir` at uninstall that was used at install.

`/var/lib/gassaway` is created by systemd through `StateDirectory=` and is readable only
by `gassaway`: `0750` directories, `0600` files. The unit binds loopback only, runs
sandboxed (`ProtectSystem=strict`, `ProtectHome=yes`, `IPAddressAllow=localhost`,
`SystemCallFilter=@system-service`), under resource ceilings (`MemoryHigh=1G`,
`MemoryMax=2G`, `TasksMax=1024`, `LimitNOFILE=65536`), and logs to the journal:

```sh
journalctl -u gassaway -f
```

The default port `4029` is the address in Amanda's `mcp.json`; another `--port` means
editing that file to match. After starting the service, `install.sh` polls `/healthz`
over the installed address and fails unless the service reports the version this tree
was built from; it names the journal command when anything fails.

## Projects and how they are named

A project's graph lives at `<data directory>/<projectId>/memory.jsonl`, where the
ID is the full lowercase SHA-256 digest of the UTF-8 canonical absolute invocation
directory. The client resolves the directory and computes the ID; the server validates
exactly 64 lowercase hexadecimal characters and uses them unchanged. Same-name projects
at different paths remain separate. Symlink aliases share one identity.

The URL is the only place the identity lives:

```text
/project/<projectId>   every MCP verb for that project's memory; GET is the SSE
                       stream, POST carries JSON-RPC, DELETE ends the session
/healthz               service status, version and project count
```

A path segment that is not exactly 64 lowercase hexadecimal characters is refused with
`400` before any project state is touched. There is no header, query parameter or second
segment that names a project, and no request falls back to a default one. A session is
scoped to the project whose server minted it: its ID is `<projectId>.<random>`, and a
request that presents it under another project's URL is refused with `404` before that
project is opened.

A project's storage appears when it first holds something. A request that only reads a
project that does not exist yet returns an empty graph and leaves the data directory
exactly as it was; the first write creates `<projectId>/` with `project.json`
(`schemaVersion: 1`, `projectId`) and `memory.jsonl`. Invalid metadata is rejected when
the project is opened and again before each write, and never silently replaced.

Removing a project's directory while the server is running deletes that project and
nothing else: its next write creates the directory and `project.json` again and the
project starts over empty. No restart is needed.

To name a project by hand, hash its resolved absolute path the way the contract does:

```sh
printf %s "$(realpath -- ~/dev/app)" | sha256sum | cut -d' ' -f1
```

There is no listing endpoint. What the server stores is what the configured data
directory holds, so `ls` and `du -sh` are the operator's view.

Two clients with the same ID share a graph and their writes are serialized, including
across a project runtime that the server has dropped and rebuilt (the write lock belongs
to the graph path, not to the in-memory runtime, and exists only while a write is in
flight). Two processes on one data directory are
not supported. There is no compatibility lookup, migration or adapter for the old
basename stores; existing data is left untouched.

`GASSAWAY_MAX_PROJECTS` bounds how many project runtimes are held at once. The least
recently used one is dropped when the bound is reached, and rebuilt from its file on the
next request; a client session that holds a dropped runtime keeps working.

## Amanda configuration

One entry in `~/.local/share/amanda/mcp.json` is trusted as Amanda's USER layer
and works in every project:

```json
{
  "schemaVersion": 1,
  "mcpServers": [
    {
      "enabled": true,
      "name": "memory",
      "url": "http://127.0.0.1:4029/project/${PROJECT_ID}"
    }
  ]
}
```

Use your server's actual address and port. Amanda expands `${PROJECT_ID}` in the URL and
supplies the same ID it uses for `~/.local/share/amanda/project/<projectId>/`, so one
entry serves every project. `${PROJECT_DIR}` and `${CWD}` name the canonical invocation
directory, and all three runtime variables override environment values.

## Configuration

The server's address and storage are command-line options, never a default:

| Option | Required | Meaning |
| --- | --- | --- |
| `--host` | yes | hostname or IP address to bind (`127.0.0.1`, `0.0.0.0`, `::1`, `localhost`); no default |
| `--port` | yes | TCP port, `1`–`65535`; no default, and `0` (kernel-assigned) is refused |
| `--project` | yes | directory holding one subdirectory per project; no default, created if missing |

Nothing else supplies them: `GASSAWAY_BIND`, `GASSAWAY_PORT` and `GASSAWAY_PROJECTS_DIR`
were retired, so a stale unit fails loudly instead of starting on an address nobody can
see. The limits keep environment variables, because they are tuning rather than identity:

| Variable | Default | Meaning |
| --- | --- | --- |
| `GASSAWAY_MAX_PROJECTS` | `1024` | bound on project runtimes held at once; the least recently used is evicted |
| `GASSAWAY_MAX_BODY_BYTES` | `4194304` | request body cap (413 past it) |
| `GASSAWAY_SESSION_TIMEOUT` | `24h` | a session with no request for this long is closed; a Go duration such as `90m` or `24h` |
| `GASSAWAY_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`; anything else refuses the start |

Exit codes: `0` ran and stopped cleanly; `2` usage error — a missing or malformed option,
in which case nothing is started and no directory is created; `1` a storage, bind or shutdown
failure. A signal drains in-flight requests for up to five seconds, then closes the
remaining connections and exits `0`: every acknowledged write is already on disk, so a
client losing its stream does not lose data.

A session normally ends when its client sends `DELETE`. A client that dies without doing
so would otherwise leave its session in memory until the next restart, so a session that
has received no request for `GASSAWAY_SESSION_TIMEOUT` is closed. An open event stream
does not count as a request. A client that returns after its session lapsed is answered
`404` and initializes a new one, as the protocol specifies; the default is long so that a
harness can sit through hours of work between two memory calls.

A request body must arrive within 30 seconds of its headers. A client that stalls is cut
off; the limit ends with the body, so it does not apply to the response stream.

`GET /healthz` on a running instance answers
`{"status":"ok","version":"0.1.0","projects":N}`. That is the liveness check and the
version query in one request: the binary has no probe mode of its own, so nothing is
started and no directory is created to ask.

## Logging

The server logs to stderr, which the unit sends to the journal:

```sh
journalctl -u gassaway -f
```

At the default level a failed MCP call produces exactly one record naming the project,
the tool, the reason and the duration; a successful call produces nothing. `debug` adds
one record per successful call. HTTP refusals (bad identity, foreign session, unknown
route) are logged with the method and status, and a storage failure is logged in full
while the client is told only that storage is unavailable.

What is never logged: tool arguments, entity names, and observation text. The error a
client was given can quote all three, so the `error` field is a redacted reason and not
that string. Arguments the schema rejected are recorded as where they failed and which
rule they broke — `invalid arguments at /properties/entities: type: has type "string",
want "array"` — without the rejected value. A missing entity is recorded as
`entity not found`, without its name. A storage failure is the server's own wording about
its own state and is recorded in full.

| Field | Meaning |
| --- | --- |
| `project` | the project ID (the SHA-256 hash in the URL) |
| `tool` | the MCP tool name for `tools/call` records |
| `error` | the redacted reason a call failed, or the panic value for a recovered panic |
| `duration_ms` | wall time of the call |
| `method`, `path`, `status`, `reason` | on HTTP-edge refusal records |

## Compatibility with the reference

`src/memory/testdata/reference-tools.json` is a committed capture of the reference
memory server's `tools/list` and `resources/list`, with its provenance in
`testdata/PROVENANCE.md`. A test compares this server's published contract against it
structurally — names, titles, annotation hints, argument and result property names,
types, `required` lists, and openness — and fails on any drift in either direction.

Two consequences are worth knowing:

- **Argument objects are open.** A payload with a field the server does not model is
  accepted and ignored, and a payload missing a required field reports that field, which
  is the actionable message a model can correct.
- **Tool results follow the reference.** `delete_entities`, `delete_observations` and
  `delete_relations` return `{success, message}`; the read tools accept the reference's
  optional `entityTypes`, `limit` and `observationLimit` arguments, and report
  `observationCount` only when an observation limit was given. `create_entities` adds a
  second text item when it skipped an existing name, so a model cannot conclude that its
  observations were stored.

- **Empty names are refused.** The reference accepts an entity whose `name` or
  `entityType` is empty, or a relation with an empty `from`, `to` or `relationType`. The
  stored format cannot represent one — it reads back as a line no tool returns — so
  `create_entities` and `create_relations` fail the whole call, write nothing, and name
  the position and the field. The published schemas are unchanged.

These shapes replace earlier, different ones (`{deleted, notFound}` and friends). No
compatibility shim is provided: this is a single-consumer service and its consumer is
updated with it.

## Storage

One JSONL file per project, in the reference format, written atomically: a save
writes a temporary file and renames it, so a reader or a crash never sees a partial
graph. Lines the server cannot parse, and fields it does not model, are preserved
by the next write.

This release uses only ID directories. It performs no migration or fallback from
basename directories. Existing data remains untouched and unused.

Backing memory up is copying the configured data directory, including project.json files.

## Security posture

The server is deployed on loopback, without authentication and without TLS. Anyone who
can reach the port can read and write every project whose ID they know. The ID is a
SHA-256 of a directory path, not a secret, so the protection is the bind address, not
the hash.

What is in place:

- **DNS-rebinding protection** from the MCP SDK: a request that arrives on a loopback
  connection with a non-loopback `Host` header is refused with `403`.
- **No CORS headers are ever sent**, so a browser cannot issue the
  `application/json` + `text/event-stream` POST that MCP requires: the preflight gets a
  `405` with no `Access-Control-Allow-Origin`, and a "simple" cross-origin POST is
  refused with `415`.
- **Project-scoped sessions**, so a session ID from one project cannot be used against
  another project's URL.
- **No Origin validation.** This is deliberate: no browser client can reach the JSON
  endpoint today, and a remote shared deployment will be fronted by a proxy that can
  enforce its own Origin policy. Revisit this at the first browser client, the first
  `Access-Control-Allow-Origin` header, or the move off loopback — then add an Origin
  allowlist at the TLS-terminating proxy, or in-process with
  `http.NewCrossOriginProtection`.

## What 0.1.0 does not do

- **No authentication and no TLS yet.** Keep it on loopback. Both are planned for the
  shared deployment; until then, anyone who reaches the port can read every project.
- **No Origin validation** — see [Security posture](#security-posture) for why, and for
  the conditions that require revisiting it.
- **No stdio transport**, no separate HTTP+SSE transport, no resource
  subscriptions, and no 2026-07-28 protocol era (the server runs the SDK's
  sessionful handler, which serves 2025-era clients — what Amanda speaks). A client that
  opens with a newer version is answered with an `UnsupportedProtocolVersion` error
  listing the versions the server does support, so it can renegotiate down.
- **No multi-process sharing** of one data directory.
- **No per-project access control**: isolation is organizational.

## License and provenance

`gassaway` is an independent Go implementation, MIT licensed (see `LICENSE`). Its
tool interface — names, titles, annotations, argument and result schemas, the
`memory://knowledge-graph` resource, the JSONL record format and the nine tools'
behavior — follows the reference memory server in
[`modelcontextprotocol/servers`](https://github.com/modelcontextprotocol/servers),
`src/memory`, which is MIT. No TypeScript source was translated; the implementation,
tests, scripts and documentation here are original.

The binary statically links Go modules whose licences require their notices to travel
with them; those notices are in
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).

This project is not affiliated with or endorsed by the Model Context Protocol
project; "MCP" is used only to describe the protocol it implements.

## Development

```sh
go test ./...            # unit and protocol tests
go test -race ./src/e2e  # black-box tier: builds the server binary and drives it over HTTP
./build.sh               # the full gate
```

Layout: `src/cmd/gassaway` is the entry point; `src/server` is the HTTP edge and the
project registry; `src/memory` is the MCP surface; `src/graph` is the JSONL store;
`src/projects` is the naming rule; `src/e2e` is the black-box suite.
