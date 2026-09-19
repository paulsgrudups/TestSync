<div align="center">

# TestSync

**Lightweight test-agent synchronization over HTTP and WebSocket.**

Store test data, share it across agents, and coordinate checkpoints in real time.

[![CI](https://github.com/paulsgrudups/TestSync/actions/workflows/ci.yml/badge.svg)](https://github.com/paulsgrudups/TestSync/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

</div>

---

Parallel test suites often need to agree on something: a shared fixture, a login
that only one agent should perform, or a moment when everyone must be ready
before the next step. TestSync is a small server that gives them one place to
store that state and a barrier to wait on.

- **Share state** — store per-test data over HTTP and fetch it from any agent
- **Synchronize** — coordinate agents with reusable WebSocket checkpoint barriers
- **Persist** — test data is durable in a local SQLite database
- **Observe** — a built-in web UI shows which checkpoint a suite is stuck on

## Contents

- [Quickstart](#quickstart)
- [Configuration](#configuration)
- [Authentication](#authentication)
- [API](#api)
  - [HTTP](#http)
  - [Monitoring and management](#monitoring-and-management)
  - [WebSocket](#websocket)
  - [Checkpoints](#checkpoints)
- [Operations](#operations)
  - [Startup failures](#startup-failures)
  - [Shutdown](#shutdown)
  - [Retention](#retention)
  - [Limits](#limits)
  - [Storage](#storage)
- [Development](#development)

## Quickstart

**1. Install Go** (1.25 or newer).

**2. Create a config directory** containing a file named `configuration.json`:

```bash
mkdir -p config
```

```json
{
  "http_port": 9104,
  "ws_port": 9105,
  "sync_client": {
    "username": "exampleUserName",
    "password": "examplePassWord"
  }
}
```

**3. Run the server**, pointing `-c` at the directory:

```bash
go run main.go -c ./config
```

> [!IMPORTANT]
> `-c` takes the **directory**, not the file: `-c ./config` reads
> `./config/configuration.json`. Credentials are mandatory — see
> [Authentication](#authentication).

Then open <http://localhost:9104/ui> to watch runs as they happen.

## Configuration

Every key except `sync_client` may be omitted. Defaults are shown below.

| Key | Default | Description |
| --- | --- | --- |
| `http_port` | `9104` | HTTP API port |
| `ws_port` | `9105` | WebSocket port |
| `logging.level` | `INFO` | `DEBUG`, `INFO`, `WARN` or `ERROR` |
| `logging.dir` | `.` | Directory for `test-sync.log` |
| `auth.mode` | `basic` | `basic`, or `none` to disable auth |
| `sync_client.username` | — | **Required** |
| `sync_client.password` | — | **Required** |
| `storage.sqlite_path` | `./testsync.db` | Database file; created if absent |
| `cleanup.interval` | `1h` | How often the janitor sweeps |
| `cleanup.retention` | `12h` | How long an idle run is kept |
| `limits.max_tests` | `10000` | Test runs registered at once |
| `limits.max_connections_per_test` | `256` | Agents attached to one run |
| `limits.max_checkpoints_per_test` | `256` | Checkpoint identifiers on one run |
| `limits.max_data_bytes` | `10485760` | 10 MiB; payload, body and frame cap |
| `checkpoint.release_lead_time` | `500ms` | How far ahead a release tells agents to resume; at most `10s` |

<details>
<summary><b>Full example configuration</b></summary>

```json
{
  "http_port": 9104,
  "ws_port": 9105,
  "logging": {
    "level": "DEBUG"
  },
  "auth": {
    "mode": "basic"
  },
  "sync_client": {
    "username": "exampleUserName",
    "password": "examplePassWord"
  },
  "storage": {
    "type": "sqlite",
    "sqlite_path": "./testsync.db"
  },
  "cleanup": {
    "interval": "1h",
    "retention": "12h"
  },
  "limits": {
    "max_tests": 10000,
    "max_connections_per_test": 256,
    "max_checkpoints_per_test": 256,
    "max_data_bytes": 10485760
  },
  "checkpoint": {
    "release_lead_time": "500ms"
  }
}
```

</details>

## Authentication

Credentials are **mandatory**. The HTTP and WebSocket servers authenticate
through the same validator, and a server with no credentials refuses to start:

```text
testsync: refusing to start: sync_client credentials are empty
```

- `auth.mode` selects the mode. `basic` is the default and requires a non-empty
  `sync_client.username` and `sync_client.password`. An unknown mode is a
  startup error.
- Credentials are compared as SHA-256 hashes with `crypto/subtle`, so the
  comparison is constant time and never leaks the length of the secret.

### Running without authentication

> [!CAUTION]
> An unauthenticated server lets anyone who can reach the ports read, overwrite
> and release the data of every test run. Use this on a local machine only.

Opt out explicitly, either in the config:

```json
{
  "auth": {
    "mode": "none"
  }
}
```

…or on the command line:

```bash
go run main.go -c ./config --insecure-no-auth
```

Both print a `WARN` banner on every startup:

```text
level=warning msg="** AUTHENTICATION IS DISABLED (auth mode \"none\")."
```

<details>
<summary><b>Migrating from a server that ran without credentials</b></summary>

A server that previously ran with no `sync_client` block — or with credentials
that silently failed to load — started fully open. It now exits non-zero at
startup. To migrate, either add real credentials to `sync_client` and configure
every agent with them, or set `"auth": {"mode": "none"}` / pass
`--insecure-no-auth` to keep the old behaviour deliberately.

Clients that already send credentials need no change.

</details>

## API

### HTTP

Base: `http://<host>:<http_port>`

| Method | Route | Description | Auth |
| --- | --- | --- | --- |
| `POST` | `/tests/{testID}` | Stores the raw request body as test data, refusing a run that already has some | Basic |
| `GET` | `/tests/{testID}` | Returns the stored raw test data | Basic |
| `PUT` | `/tests/{testID}` | Replaces the stored test data, whether or not there was any | Basic |
| `DELETE` | `/tests/{testID}` | Deletes the run and its stored data — `204`, with `X-TestSync-Connections-Dropped` | Basic |
| `GET` | `/health` | Returns `{"status":"ok"}` | None |

Errors are JSON — `{"code": <int>, "error": "<message>"}` — while successful
reads return raw bytes. Failures that a client may want to branch on carry a
stable `"reason"` as well, such as `run_not_found` or `no_round_in_progress`.

### Monitoring and management

Live state and the operator overrides for it, behind the same credentials.

| Method | Route | Description |
| --- | --- | --- |
| `GET` | `/api/v1/runs` | Every known run with its agent, checkpoint and data counters |
| `GET` | `/api/v1/runs/{testID}` | One run: its agents, and each checkpoint with identifier, current round, target count and joined members |
| `GET` | `/api/v1/runs/{testID}/data` | The run's stored payload, as an opaque download |
| `POST` | `/api/v1/runs/{testID}/checkpoints/release` | Force-releases the round in progress on the checkpoint named in the body |
| `DELETE` | `/api/v1/runs/{testID}/connections/{connID}` | Disconnects one agent, freeing its slot in every barrier it joined |
| `DELETE` | `/api/v1/runs/{testID}` | Deletes the run and its stored data, as `DELETE /tests/{testID}` does |
| `GET` | `/ui` | Auto-refreshing operator page |

The UI is embedded in the binary and loads nothing from the network, so it works
on an air-gapped CI box. It is served through the standard `401` challenge, so a
browser will prompt for the same credentials.

The release body is `{"identifier": "<checkpoint>", "note": "<optional>"}`. The
waiting agents receive the usual checkpoint release, with
`"reason": "operator_released"` and `"finished": false`: they resume together,
but the barrier they were waiting for was not met. The optional `note` (at most
128 bytes) reaches them in the release's `note` field, for whoever reads the
logs; `reason` stays the fixed value so agents can branch on it.

> [!NOTE]
> `/api/v1/runs/{testID}/data` is the only route that returns stored contents.
> Every other response reports the **size** of a run's data rather than its
> contents. Agents are numbered per run from zero in arrival order; numbers are
> not reused, so a gap means an agent disconnected.

### WebSocket

Base: `ws://<host>:<ws_port>`

| Method | Route | Description | Auth |
| --- | --- | --- | --- |
| `GET` | `/register/{testID}` | Establishes a connection for a test run | Basic |

> [!WARNING]
> **Deprecated fallback.** Clients that cannot set headers may pass `username`
> and `password` as query parameters. Query strings leak into proxy and access
> logs, so this path logs a deprecation warning on every use and will be
> removed. Prefer the `Authorization` header.

The full specification — every message, error code and close code, with
examples — is in **[PROTOCOL.md](PROTOCOL.md)**. This is protocol **v1**;
clients should offer the `testsync.v1` subprotocol.

Every frame is a JSON text frame in one envelope, and every command gets exactly
one reply: its result, or an `error`. An optional `id` is echoed on the reply.

```json
{"command": "read_data", "id": 7}
{"command": "read_data", "id": 7, "content": {"user": "alice"}}
```

| Command | Reply |
| --- | --- |
| `update_data` | Stores `content` as the run's payload; replies `{"bytes": <int>}` |
| `read_data` | The stored payload as `content`, or `null`. Only JSON payloads travel over WebSocket; read others over HTTP |
| `get_connection_count` | `{"count": <int>}`, counting only live connections |
| `wait_checkpoint` | Joins a checkpoint barrier; the reply is the release |
| `close` | No reply; the server closes with `1000` after answering earlier commands |

### Checkpoints

A checkpoint is a barrier: every agent that joins waits until the round it
joined ends, and they are all told to resume at the same moment.

```json
{"command": "wait_checkpoint", "id": "j1",
 "content": {"identifier": "login-complete", "target_count": 4, "timeout_ms": 60000}}
```

`target_count` is how many distinct connections the round waits for;
`timeout_ms` is optional (default 60s, clamped at 30m). The first agent of a
round fixes both. When the round ends, every participant receives:

```json
{"command": "wait_checkpoint", "id": "j1", "content": {
  "identifier": "login-complete", "finished": true, "reason": "complete",
  "note": "", "generation": 1, "joined": 4, "target": 4,
  "start_in_ms": 498, "server_time_ms": 1789037384226, "start_at": 1789037384726}}
```

**Sleep `start_in_ms` from receipt, then resume.** It is relative so that
agents on machines whose clocks disagree still resume together; `start_at` is
deprecated for exactly that reason.

`reason` is one of `complete`, `timeout`, `participant_lost` or
`operator_released`, and `finished` is `true` only for `complete`.

> [!IMPORTANT]
> A release with `finished: false` means the agents were **not** synchronized,
> and the suite should fail loudly rather than carry on.

**Barriers are reusable.** Once a round ends, the identifier immediately starts
a fresh round, so a looping suite uses the same identifier every iteration. A
round also ends early with `participant_lost` when a disconnect leaves too few
agents to reach its target.

### Limits

Each limit is configurable and has a documented rejection: **exceeding one is
always reported, never silently dropped.**

| Setting | Bounds | Rejection |
| --- | --- | --- |
| `max_tests` | Test runs registered at once | HTTP `503` `{"code":503,"error":"Too many active test runs; retry once running suites finish"}`. A WebSocket registration for a **new** test ID is refused the same way before the upgrade; if the limit is reached during the upgrade, the connection closes with **1013 (Try Again Later)** |
| `max_connections_per_test` | Agents attached to one run | The connection closes with **1013** and reason `connection limit reached for this test run`. Connections already accepted are unaffected, and a slot freed by a departing agent is reusable |
| `max_checkpoints_per_test` | Checkpoint identifiers on one run | `wait_checkpoint` is answered with an `error` carrying code `checkpoint_limit_reached`. The connection stays usable, and existing identifiers keep working |
| `max_data_bytes` | A stored payload, an HTTP body and a single WebSocket frame | `POST /tests/{id}` answers `413` `{"code":413,"error":"Request data too large"}` and stores nothing. `update_data` is answered with an `error` carrying code `payload_too_large`. A wildly oversized frame is rejected from its header with close code **1009** |

The three payload caps are deliberately one number: a payload accepted on one
path is accepted on all of them.

A limit that is omitted or set to `0` uses the default. A negative value is a
startup error. There is no "unlimited" setting — an unbounded server is what
these exist to prevent.

A refused WebSocket command is answered with an `error` reply carrying a stable
`code`, such as `payload_too_large`; the connection stays usable. See
[PROTOCOL.md](PROTOCOL.md#errors) for every code.

### Storage

Test data is persisted in a local SQLite database, which is the single source of
truth.

- `storage.sqlite_path` sets the database file.
- The database and any missing parent directories are created on startup, so a
  first run needs no setup step.
- If the configured path holds a file that is not a usable database, it is moved
  aside to `<path>.corrupt-<timestamp>` and a fresh database is created in its
  place. Startup logs a warning when this happens.
- `storage.type` is accepted for backwards compatibility. `sqlite` is the only
  supported value; any other value logs a warning and is ignored.

## Development

```bash
go build ./...
go vet ./...
gofmt -l .                 # must print nothing
golangci-lint run ./...    # must report 0 issues
go test -race ./...
```

Changes to concurrent code are additionally expected to survive:

```bash
go test -race -count=20 ./api/...
```

### End-to-end check

[`usage/e2e/main.go`](usage/e2e/main.go) drives a real server through the HTTP
and WebSocket flows:

```bash
go run ./usage/e2e
```

| Variable | Default |
| --- | --- |
| `TESTSYNC_HTTP_URL` | `http://localhost:9104` |
| `TESTSYNC_WS_URL` | `ws://localhost:9105` |
| `TESTSYNC_USER` | `exampleUserName` |
| `TESTSYNC_PASS` | `examplePassWord` |

The credentials must match the server's `sync_client` block. The Nightwatch
example ([`usage/tests/connection.js`](usage/tests/connection.js)) reads the same
two variables and sends them in the `Authorization` header.

> [!NOTE]
> The E2E script uses a fixed test ID and is **not idempotent** — run it against
> a fresh database, or it will conflict with the run it created last time.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow, and
[SECURITY.md](SECURITY.md) for how to report a vulnerability privately.

## License

[Apache-2.0](LICENSE)
