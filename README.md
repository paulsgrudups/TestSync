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
  - [Monitoring](#monitoring)
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
| `POST` | `/tests/{testID}` | Stores the raw request body as test data | Basic |
| `GET` | `/tests/{testID}` | Returns the stored raw test data | Basic |
| `GET` | `/health` | Returns `{"status":"ok"}` | None |

Errors are JSON — `{"code": <int>, "error": "<message>"}` — while successful
reads return raw bytes.

### Monitoring

Read-only views of live state, behind the same credentials.

| Method | Route | Description |
| --- | --- | --- |
| `GET` | `/api/v1/runs` | Every known run with its agent, checkpoint and data counters |
| `GET` | `/api/v1/runs/{testID}` | One run: its agents, and each checkpoint with identifier, current round, target count and joined members |
| `GET` | `/ui` | Auto-refreshing operator page for the two endpoints above |

The UI is embedded in the binary and loads nothing from the network, so it works
on an air-gapped CI box. It is served through the standard `401` challenge, so a
browser will prompt for the same credentials.

> [!NOTE]
> These routes only read. They never release a barrier or change stored data,
> and they report the **size** of a run's stored data rather than its contents.
> Agents are numbered per run from zero in arrival order; numbers are not
> reused, so a gap means an agent disconnected.

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

Every message uses the same envelope:

```json
{
  "command": "<string>",
  "content": {}
}
```

| Command | Behaviour |
| --- | --- |
| `read_data` | Replies with the raw stored data |
| `update_data` | Replaces stored data. A payload over `limits.max_data_bytes` is refused |
| `get_connection_count` | Replies with `{"count": <int>}`, counting only live connections. A disconnected agent stops being counted within one round-trip |
| `wait_checkpoint` | Joins a checkpoint barrier — see below |
| `close` | Closes the connection |

### Checkpoints

A checkpoint is a barrier: every agent that joins waits until the round it
joined ends, and they are all told to resume at the same moment.

**Request:**

```json
{
  "identifier": "login-complete",
  "target_count": 4,
  "timeout_ms": 60000
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `identifier` | yes | Names the barrier. Must not be empty |
| `target_count` | yes | How many distinct connections the round waits for. At least `1` |
| `timeout_ms` | no | Bounds the wait. Omitted or `0` means 60s; above 30m is clamped; negative is rejected |

The deadline is measured from the first agent's arrival, and the first agent of
a round also fixes its `target_count` and `timeout_ms` for the other
participants.

**Reply**, sent to every participant when the round ends:

```json
{
  "identifier": "login-complete",
  "finished": true,
  "start_at": 1788209831733,
  "reason": "complete",
  "generation": 1,
  "joined": 4,
  "target": 4
}
```

| Field | Meaning |
| --- | --- |
| `identifier` | The barrier that ended |
| `finished` | `true` only when every expected agent arrived |
| `start_at` | Wall-clock milliseconds at which participants should resume |
| `reason` | `complete`, `timeout`, or `participant_lost` |
| `generation` | Round number, counting from `1` for each identifier |
| `joined` / `target` | How many agents arrived, and how many were expected |

> [!IMPORTANT]
> Only `complete` reports `finished: true`. A `timeout` or `participant_lost`
> round means the agents were **not** synchronized, and the suite should fail
> loudly rather than carry on.

`identifier`, `finished` and `start_at` are sent for every outcome, so a client
that ignores the other fields keeps working.

**Barriers are reusable.** Once a round ends, the identifier immediately starts
a fresh, empty round, so a looping suite calls `wait_checkpoint` with the same
identifier every iteration and each round blocks on its own. Unique
per-iteration identifiers are not needed.

A round also ends when it can no longer succeed: if a connection disconnects and
fewer connections remain than the round's `target_count`, everyone still waiting
is released with `participant_lost` rather than waiting for an agent that is
never coming back. An agent that disconnects *before* the others join is not
detectable that way, so the round's timeout is the backstop.

## Operations

### Startup failures

A configuration the server cannot run with is reported as one line on stderr,
and the process exits with status `1`:

```text
testsync: no configuration file at ./config/configuration.json: create it, or point -c at the directory holding it
testsync: could not read ./config/configuration.json: unexpected end of JSON input
testsync: invalid logging.level "VERBOSE": use DEBUG, INFO, WARN or ERROR
testsync: invalid configuration in ./config/configuration.json: http_port is 70000; it must be between 1 and 65535
testsync: http server on port 9104: listen tcp :9104: bind: address already in use
testsync: websocket server on port 9105: listen tcp :9105: bind: address already in use
```

### Shutdown

On `SIGINT`/`SIGTERM` the server stops accepting, lets in-flight requests
finish, tells every connected agent it is going away with WebSocket close code
**1012 (Service Restart)**, stops the janitor, and closes the database last. The
whole sequence is bounded at 15 seconds.

An agent can therefore tell a deploy from a crash: `1012` means "reconnect",
while a dropped socket (`1006`) does not.

### Retention

Test runs are held in memory for as long as they are useful, then reclaimed —
together with their stored data — by a background janitor.

- `cleanup.retention` is how long a run with **no connected agents** is kept.
- `cleanup.interval` is how often the janitor sweeps. It also sweeps once at
  startup, so data left by a previous process is reclaimed immediately.
- Both are duration strings: `"90s"`, `"30m"`, `"12h"`. An unparseable value is
  a startup error.

> [!NOTE]
> A run whose agents are still connected is **never** swept, however old it is,
> and neither is its stored data.

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

#### The `error` reply

A refused command is answered on the same connection, in the standard envelope:

```json
{
  "command": "error",
  "content": {
    "code": "payload_too_large",
    "error": "<human-readable reason>"
  }
}
```

`code` is one of `payload_too_large`, `checkpoint_limit_reached`,
`test_limit_reached` or `connection_limit_reached`. It is stable and safe to
branch on; `error` is for logs and for whoever reads the failed run.

`error` is a reply, never a request: a client that ignores unknown commands
keeps working exactly as before.

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
