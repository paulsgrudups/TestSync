# WARNING
As of now, this repository is experimental and should mostly used as reference 
or to create your own fork.
The tool may contain critical issues. 
Future versions may introduce breaking changes.

# TestSync
Lightweight test agent synchronization over HTTP and WebSocket. Store test data, share it across agents, and coordinate checkpoints in real time.

> Status: experimental. Use at your own risk.

## What it does
- Store per-test data via HTTP and fetch it later
- Coordinate agents with WebSocket checkpoints
- Persistence via SQLite

## Quickstart
1) Install Go
2) Create a config directory containing a file named `configuration.json`
3) Run the server, pointing `-c` at that directory

The `-c` flag takes the **directory**, not the file: `-c ./config` reads
`./config/configuration.json`. Credentials are mandatory — see
[Authentication](#authentication).

Example config:
```
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

Every key except `sync_client` may be omitted; the defaults above are what the
server uses. `cleanup` and `limits` are documented under
[Retention](#retention) and [Limits](#limits).

Run:
- go run main.go -c ./config

### Startup failures
A configuration the server cannot run with is reported as one line on stderr,
and the process exits with status 1. Earlier versions panicked with a runtime
stack trace for each of these:

```
testsync: no configuration file at ./config/configuration.json: create it, or point -c at the directory holding it
testsync: could not read ./config/configuration.json: unexpected end of JSON input
testsync: invalid logging.level "VERBOSE": use DEBUG, INFO, WARN or ERROR
testsync: invalid configuration in ./config/configuration.json: http_port is 70000; it must be between 1 and 65535
testsync: http server on port 9104: listen tcp :9104: bind: address already in use
testsync: websocket server on port 9105: listen tcp :9105: bind: address already in use
```

A WebSocket port that was already in use used to be ignored entirely: the
process kept running with no WebSocket server and said nothing about it.

### Shutdown
On `SIGINT`/`SIGTERM` the server stops accepting, lets requests that are
already in flight finish, tells every connected agent that it is going away
with WebSocket close code **1012 (Service Restart)**, stops the janitor, and
closes the database last. The whole sequence is bounded at 15 seconds.

An agent can therefore tell a deploy from a crash: 1012 means "reconnect",
while a dropped socket (1006) does not. Earlier versions closed the database
first, so requests in flight during a restart failed with
`sql: database is closed`, and agents were never told anything.

## Authentication
Credentials are **mandatory**. Both the HTTP and the WebSocket server
authenticate through the same validator, and a server that has no credentials
refuses to start:

```
testsync: refusing to start: sync_client credentials are empty
```

- `auth.mode` selects the mode. `basic` is the default and requires a non-empty
  `sync_client.username` and `sync_client.password`. An unknown mode is a
  startup error.
- Credentials are compared as SHA-256 hashes with `crypto/subtle`, so the
  comparison is constant time and never leaks the length of the secret.
- Earlier versions started with an open server when `sync_client` was missing,
  misspelled, or failed to unmarshal. That fail-open behaviour is gone; see
  "Breaking change" below.

### Running without authentication (development only)
An unauthenticated server lets anyone who can reach the ports read, overwrite
and release the data of every test run. If that is acceptable on a local
machine, opt out explicitly, either in the config:

```
{
  "auth": {
    "mode": "none"
  }
}
```

or on the command line:

- go run main.go -c ./config --insecure-no-auth

Both print a `WARN` banner on every startup:

```
level=warning msg="** AUTHENTICATION IS DISABLED (auth mode \"none\")."
```

### Breaking change
A server that previously ran with no `sync_client` block (or with credentials
that silently failed to load) started fully open. It now exits non-zero at
startup. To migrate, either add real credentials to `sync_client` and configure
every agent with them, or set `"auth": {"mode": "none"}` / pass
`--insecure-no-auth` to keep the old behaviour deliberately. Clients that
already send credentials need no change.

## API

### HTTP
Base: http://<host>:<http_port>

Routes:
- POST /tests/{testID}
  - Stores raw request body as test data
  - Auth: Basic Auth using sync_client
- GET /tests/{testID}
  - Returns stored raw test data
  - Auth: Basic Auth using sync_client
- GET /health
  - Returns {"status":"ok"}
  - Auth: none

Monitoring (read-only):
- GET /api/v1/runs
  - Lists every known test run with its agent, checkpoint and data counters
  - Auth: Basic Auth using sync_client
- GET /api/v1/runs/{testID}
  - Returns one run: its agent connections and each checkpoint with its
    identifier, current round, target count and joined members
  - Auth: Basic Auth using sync_client
- GET /ui
  - Auto-refreshing page showing the run list and per-run detail, so an
    operator can see which checkpoint a suite is stuck on. Embedded in the
    binary; it loads nothing from the network
  - Auth: Basic Auth using sync_client, through the standard 401 challenge

These routes only read. They never release a barrier or change stored data, and
they report the size of a run's stored data rather than its contents. Agents are
numbered per run from zero in arrival order; the numbers are not reused, so a
gap means an agent disconnected.

Responses:
- Errors are JSON: {"code": <int>, "error": "<message>"}
- Success responses return raw bytes

### WebSocket
Base: ws://<host>:<ws_port>

Routes:
- GET /register/{testID}
  - Establishes WS connection for a test run
  - Auth: Basic Auth, using the same credentials as the HTTP routes
  - Fallback (deprecated): query params username/password for clients without
    header support. Query strings leak into proxy and access logs, so this path
    logs a deprecation warning on every use and will be removed; prefer the
    Authorization header.

Message format:
```
{
  "command": "<string>",
  "content": <json>
}
```

Commands:
- read_data: reply with raw stored data
- update_data: replace stored data with provided content. A payload over
  `limits.max_data_bytes` is refused; see [Limits](#limits)
- get_connection_count: reply with {"count": <int>}, counting only connections
  that are still alive. A disconnected agent stops being counted within one
  command round-trip.
- wait_checkpoint: join a checkpoint barrier
- close: close the WS connection

### Checkpoints
A checkpoint is a barrier: every agent that joins it waits until the round it
joined ends, and they are all told to resume at the same moment.

Request content:
```
{
  "identifier": "<string>",
  "target_count": <int>,
  "timeout_ms": <int, optional>
}
```

- `identifier` names the barrier. It must not be empty.
- `target_count` is how many distinct connections the round waits for. It must
  be at least 1.
- `timeout_ms` (optional, added for CONC-6) bounds how long the round may wait.
  Omitted or `0` means the server default of 60s; anything above the server
  maximum of 30m is clamped to it; a negative value is rejected. The deadline
  is measured from the first agent's arrival, and the first agent of a round
  also fixes its `target_count` and `timeout_ms` for the other participants.

Reply content, sent to every participant of the round when it ends:
```
{
  "identifier": "<string>",
  "finished": <bool>,
  "start_at": <unix milliseconds>,
  "reason": "complete" | "timeout" | "participant_lost",
  "generation": <int>,
  "joined": <int>,
  "target": <int>
}
```

- `identifier`, `finished` and `start_at` are unchanged. `finished` is still
  true only when every expected agent arrived, and `start_at` is still the
  wall-clock millisecond at which the participants should resume. Both are sent
  for every outcome, so a client that ignores the fields below keeps working.
- `reason` (new) says why the round ended: `complete` when the target was
  reached, `timeout` when the deadline passed first, `participant_lost` when a
  connection went away and left too few agents to reach the target. Only
  `complete` reports `finished: true`; the other two mean the run was **not**
  synchronized and the agent should fail loudly instead of carrying on.
- `generation` (new) is the round number, counting from 1 for each identifier.
- `joined` and `target` (new) are how many agents had arrived and how many were
  expected, which is what makes a failed barrier diagnosable.

Barriers are reusable. Once a round ends, the identifier immediately starts a
fresh, empty round, so a looping suite calls `wait_checkpoint` with the same
identifier every iteration and each round blocks on its own. Earlier versions
fired an identifier exactly once and released every later join immediately,
which desynchronized looping suites silently; unique per-iteration identifiers
are no longer needed.

A round also ends when it can no longer succeed: if a connection disconnects
and fewer connections remain than the round's `target_count`, everyone still
waiting is released with `participant_lost` rather than waiting for an agent
that is never coming back. An agent that disconnects before the others join is
not detectable that way, so the round's timeout is the backstop.

## Retention
Test runs are held in memory for as long as they are useful and are then
reclaimed, together with their stored data, by a background janitor.

- `cleanup.retention` is how long a run with **no connected agents** is kept.
  Defaults to `12h`.
- `cleanup.interval` is how often the janitor sweeps. Defaults to `1h`. It also
  sweeps once at startup, so data left behind by a previous process is
  reclaimed immediately rather than one interval later.
- Both are duration strings: `"90s"`, `"30m"`, `"12h"`. An unparseable value is
  a startup error.

A run whose agents are still connected is **never** swept, however old it is,
and neither is its stored data. Earlier versions deleted a run purely by age:
a suite still running after the window had its state deleted from underneath
it, and the agents that arrived afterwards ended up on a second, empty run
with a different connection count and barriers the first half could not join.

The sweep also no longer starts as a side effect of registering the HTTP
routes, so an in-process test harness can build the router as often as it likes
without leaking a background worker per call.

## Limits
Nothing used to bound the number of runs, the agents on one run, the barriers
on one run, or the size of a payload, so any authorized client could exhaust
the server's memory. Each limit below is configurable and has a documented
rejection: **exceeding one is always reported, never silently dropped**.

| Setting | Default | Bounds | Rejection |
|---|---|---|---|
| `limits.max_tests` | 10000 | Test runs registered at once | HTTP `503` with `{"code":503,"error":"Too many active test runs; retry once running suites finish"}`. A WebSocket registration for a **new** test ID is refused the same way before the connection is upgraded; if the limit is reached during the upgrade, the connection is closed with **1013 (Try Again Later)** |
| `limits.max_connections_per_test` | 256 | Agents attached to one run | The WebSocket connection is closed with **1013 (Try Again Later)** and reason `connection limit reached for this test run`. Connections already accepted are unaffected, and a slot freed by a departing agent is reusable |
| `limits.max_checkpoints_per_test` | 256 | Distinct checkpoint identifiers on one run | `wait_checkpoint` is answered with an `error` message carrying code `checkpoint_limit_reached`. The connection stays usable, and identifiers that already exist keep working. Barriers are reusable, so a looping suite needs one identifier per barrier, not one per iteration |
| `limits.max_data_bytes` | 10485760 (10 MiB) | A stored payload, an HTTP request body and a single WebSocket frame | `POST /tests/{id}` answers `413` with `{"code":413,"error":"Request data too large"}` and stores nothing. `update_data` is answered with an `error` message carrying code `payload_too_large`. A frame that is wildly oversized (more than the limit plus a 1 KiB envelope allowance) is still rejected from its header with close code **1009** |

The three payload caps are deliberately one number: a payload that is accepted
on one path is accepted on all of them.

A limit that is omitted or set to `0` uses the default. A negative value is a
startup error. There is no "unlimited" setting; an unbounded server is what
these exist to prevent.

### The `error` reply
A refused command is answered on the same connection, in the standard
envelope:

```
{
  "command": "error",
  "content": {
    "code": "payload_too_large" | "checkpoint_limit_reached" |
            "test_limit_reached" | "connection_limit_reached",
    "error": "<human-readable reason>"
  }
}
```

`error` is a new reply, not a new request: a client that ignores unknown
commands keeps working exactly as before. `code` is stable and safe to branch
on; `error` is for logs and for whoever reads the failed run.

## Storage
Test data is persisted in a local SQLite database. There is no in-memory
backend; the database is the single source of truth.

- `storage.sqlite_path` sets the database file. Defaults to `./testsync.db`.
- The database and any missing parent directories are created on startup when
  they do not already exist, so a first run needs no setup step.
- If the configured path holds a file that is not a usable database, it is
  moved aside to `<path>.corrupt-<timestamp>` and a fresh database is created
  in its place. Startup logs a warning when this happens.
- `storage.type` is accepted for backwards compatibility. `sqlite` is the only
  supported value; any other value logs a warning and is ignored.

## E2E validation
E2E script: [usage/e2e/main.go](usage/e2e/main.go)

Environment variables:
- TESTSYNC_HTTP_URL (default: http://localhost:9104)
- TESTSYNC_WS_URL (default: ws://localhost:9105)
- TESTSYNC_USER (default: exampleUserName)
- TESTSYNC_PASS (default: examplePassWord)

The credentials must match the server's `sync_client` block. The Nightwatch
example ([usage/tests/connection.js](usage/tests/connection.js)) reads the same
two variables and sends them in the `Authorization` header.

## Development
- go test ./...
- go run main.go -c ./config
