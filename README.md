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

The tool surface follows the reference memory server: `create_entities`,
`create_relations`, `add_observations`, `delete_entities`, `delete_observations`,
`delete_relations`, `read_graph`, `search_nodes`, `open_nodes`, plus the
`memory://knowledge-graph` resource. The on-disk format is the same JSONL, so an
existing `memory.jsonl` can be imported without conversion.

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
`SystemCallFilter=@system-service`), and logs to the journal:

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
`400` before the project is created. There is no header, query parameter or second
segment that names a project, and no request falls back to a default one. Each hash
directory contains `project.json` with `schemaVersion: 1` and `projectId`; invalid
metadata is rejected when opening the store.

To name a project by hand, hash its resolved absolute path the way the contract does:

```sh
printf %s "$(realpath -- ~/dev/app)" | sha256sum | cut -d' ' -f1
```

There is no listing endpoint. What the server stores is what the configured data
directory holds, so `ls` and `du -sh` are the operator's view.

Two clients with the same ID share a graph and their writes are serialized. Two
processes on one data directory are not supported. There is no compatibility lookup,
migration or adapter for the old basename stores; existing data is left untouched.

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
see. Two limits keep environment variables, because they are tuning rather than identity:

| Variable | Default | Meaning |
| --- | --- | --- |
| `GASSAWAY_MAX_PROJECTS` | `1024` | cap on projects open in one process (429 past it) |
| `GASSAWAY_MAX_BODY_BYTES` | `4194304` | request body cap (413 past it) |

Exit codes: `0` ran and stopped cleanly; `2` usage error — a missing or malformed option,
in which case nothing is started and no directory is created; `1` a storage, bind or
shutdown failure.

`GET /healthz` on a running instance answers
`{"status":"ok","version":"0.1.0","projects":N}`. That is the liveness check and the
version query in one request: the binary has no probe mode of its own, so nothing is
started and no directory is created to ask.

## Storage

One JSONL file per project, in the reference format, written atomically: a save
writes a temporary file and renames it, so a reader or a crash never sees a partial
graph. Lines the server cannot parse, and fields it does not model, are preserved
by the next write.

This release uses only ID directories. It performs no migration or fallback from
basename directories. Existing data remains untouched and unused.

Backing memory up is copying the configured data directory, including project.json files.

## What 0.1.0 does not do

- **No authentication.** Keep it on loopback, or put an authenticating proxy in
  front. Anyone who reaches the port can read every project.
- **No stdio transport**, no separate HTTP+SSE transport, no resource
  subscriptions, and no 2026-07-28 protocol era (the server runs the SDK's
  sessionful handler, which serves 2025-era clients — what Amanda speaks).
- **No multi-process sharing** of one data directory.
- **No per-project access control**: isolation is organizational.

## License and provenance

`gassaway` is an independent Go implementation, MIT licensed (see `LICENSE`). Its
tool interface — names, the `memory://knowledge-graph` resource, the JSONL record
format and the nine tools' behavior — follows the reference memory server in
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
