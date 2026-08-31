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
2) Create configuration
3) Run the server

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
  }
}
```

Run:
- go run main.go -c ./config

## Authentication
Credentials are **mandatory**. Both the HTTP and the WebSocket server
authenticate through the same validator, and a server that has no credentials
refuses to start:

```
Refusing to start: sync_client credentials are empty
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
- update_data: replace stored data with provided content
- get_connection_count: reply with {"count": <int>}
- wait_checkpoint: register checkpoint barrier
- close: close the WS connection

Checkpoint content:
```
{
  "identifier": "<string>",
  "target_count": <int>
}
```

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
