# Contributing to TestSync

Thanks for taking an interest in TestSync.

TestSync is a small synchronization server for parallel automated test agents.
Agents store per-test data over HTTP and coordinate over WebSocket checkpoint
barriers: each agent sends `wait_checkpoint` with an identifier and a target
count, and the server releases all of them once enough have arrived.

The project started as a bachelor-thesis proof of concept and is being turned
into something dependable. That history matters for contributors in two ways:

- The internals are still moving. Package boundaries, configuration keys and
  HTTP routes can change before 1.0.
- The WebSocket wire format is **not** free to move. Real test suites depend on
  it, so it is treated as a compatibility surface (see
  [The WebSocket protocol is a compatibility surface](#the-websocket-protocol-is-a-compatibility-surface)).

If you plan anything larger than a small fix, please open an issue first.
Changes to the barrier and connection logic in particular are easier to agree
on as a design sketch than as a finished pull request.

## Prerequisites

- **Go.** The module targets the version in [`go.mod`](go.mod) (currently
  `1.25.5`) and CI builds with `1.25.x`.
- **A C compiler** (`gcc` or `clang`) on `PATH`. The SQLite driver
  (`modernc.org/sqlite`) is pure Go and needs nothing, but Go's race detector
  does — without a C compiler, `go test -race ./...` will not run, and `-race`
  is part of the verification bar below.
- **`golangci-lint` v2.13.2**, the version pinned by
  [`.github/workflows/ci.yml`](.github/workflows/ci.yml). Other versions
  disagree with [`.golangci.yml`](.golangci.yml) about which linters exist, so
  match the pin.
- **Node.js**, only if you touch the Nightwatch example under `usage/`.

## Repository layout

| Path | What lives there |
| --- | --- |
| `main.go` | Startup: config load, auth setup, HTTP and WebSocket servers, shutdown |
| `api/` | HTTP router and route wiring |
| `api/auth/` | Credential validator and Basic Auth middleware, shared by HTTP and WS |
| `api/runs/` | The core: test runs, connection registry, checkpoint barriers |
| `api/ws/` | WebSocket server, upgrade path and command dispatch |
| `api/monitor/` | Read-only `/api/v1/runs` endpoints and the embedded `/ui` page |
| `storage/` | `DataStore` interface and its SQLite implementation |
| `utils/` | Configuration loading, defaults, validation, HTTP helpers |
| `wsutil/` | WebSocket client wrapper and status types |
| `internal/storagetest/` | Test helper that hands a test its own temporary database |
| `usage/` | Real client examples: the Go E2E runner and the Nightwatch example |

Only test-run *data* is persisted. Connections and checkpoints live in the
server process, so restarting the server drops every in-flight barrier.

## Running the server locally

TestSync reads `configuration.json` from a **directory** given by `-c` /
`--configDir`, which defaults to `./config`. That directory is gitignored, so
create your own:

```bash
mkdir -p config
$EDITOR config/configuration.json   # see the example config in README.md
go run main.go -c ./config
```

### Credentials are mandatory — this is the first thing that trips new contributors

A server whose `sync_client` credentials are empty **refuses to start**:

```
Refusing to start: sync_client credentials are empty
```

This is deliberate. Earlier versions started wide open when the `sync_client`
block was missing or failed to unmarshal, and that fail-open behaviour is gone.
If you want an unauthenticated server on your own machine, opt out explicitly,
either in the config:

```json
{ "auth": { "mode": "none" } }
```

or on the command line:

```bash
go run main.go -c ./config --insecure-no-auth
```

Both paths print a `WARN` banner on every startup. Please do not "fix" a
failing startup by reintroducing a silent fallback to no authentication; a
change that lets the server run unauthenticated without an explicit opt-out
will not be merged.

## The verification bar

This project holds a stricter bar than most repositories of its size. Run all
of these locally before opening a pull request:

```bash
go build ./...
go vet ./...
gofmt -l .                 # must print nothing
golangci-lint run ./...    # must report 0 issues
go test -race ./...        # must be green
```

Notes on each:

- `gofmt -l .` printing a filename is a failure, not a suggestion. The lint
  config also runs `goimports` (with `github.com/paulsgrudups/testsync` as the
  local prefix, so intra-project imports form their own group) and `golines`
  with a 120-character limit.
- `golangci-lint run ./...` is expected at **zero** issues, not "no new
  issues". `.golangci.yml` is a strict configuration and the project holds
  itself to a clean run against it; keep it that way. Prefer restructuring the code over adding a
  `//nolint` directive, and when a directive is genuinely the right answer,
  scope it to one linter on one line and say why.
- **Concurrency changes get more.** If you touch `api/runs/`, `api/ws/` or
  `wsutil/`, also run:

  ```bash
  go test -race -count=20 ./api/...
  ```

  A barrier race that shows up once in twenty runs is still a bug, and this is
  the cheapest way to find one before a user does.

CI (build, `go test ./...`, `go vet`, `golangci-lint`) must be green on your
pull request. CI does not currently run `-race` or the end-to-end check, so
those two are on you locally.

## Tests

### A regression test must be verified to fail before the fix

This is a house rule, not a formality. For any bug fix:

1. Write the test **first**, against the unfixed tree.
2. Run it and watch it fail. Keep the failure output.
3. Apply the fix.
4. Run it again and watch it pass.

A test that passes both before and after the fix proves nothing about the bug,
and we would rather have no test than a test that gives false confidence.
Please paste the "before" failure into the pull request description — that is
the evidence the test is real.

### Conventions

- Tests that need persistence use `internal/storagetest.NewStore(t)`, which
  creates a SQLite database in the test's own `t.TempDir()` and closes it via
  `t.Cleanup`. Tests do not share a database, and none of them should write to
  the repository working directory.
- Prefer synchronizing on channels, `sync.WaitGroup` or a polling helper over
  `time.Sleep`. Sleeps make the suite slow and turn real races into flakes
  under `-count=20`.
- Barrier tests should assert on the whole reply — `reason`, `joined` and
  `target`, not only `finished`. Most barrier bugs are visible as the wrong
  `reason`.

## The WebSocket protocol is a compatibility surface

Agents embedded in someone's test suite are hard to upgrade in lockstep with
the server, so the wire format follows two rules:

1. **Existing fields do not change name, type or meaning.** `identifier`,
   `finished`, `start_at`, `command` and `content` mean today what they meant
   in the previous release.
2. **New information goes into new, optional fields.** `reason`, `generation`,
   `joined` and `target` were added to the checkpoint reply exactly this way: a
   client that reads only `finished` and `start_at` still works.

Two clients in this repository are treated as real users of that protocol, and
both must keep working after your change:

- [`usage/e2e/main.go`](usage/e2e/main.go) — the Go end-to-end runner.
- [`usage/tests/connection.js`](usage/tests/connection.js) — the Nightwatch
  example, which speaks the protocol by hand from JavaScript.

If a change genuinely cannot be made compatible, say so explicitly in the pull
request, update both clients in the same change, and describe the migration for
an existing suite.

One deliberate exception: passing WebSocket credentials as `?username=&password=`
query parameters is a **deprecated** fallback for clients that cannot set
headers. It still works and logs a deprecation warning on every use. Do not
build anything new on it; use the `Authorization` header.

## The end-to-end check

`usage/e2e/main.go` drives a running server through the whole flow: HTTP write,
HTTP read-back, WebSocket connect, `read_data`, `get_connection_count` and a
`wait_checkpoint` round. It is a plain program, not a `go test`, so `go test
./...` does not run it.

In one terminal:

```bash
go run main.go -c ./config
```

In another:

```bash
go run ./usage/e2e
```

It prints `E2E flow completed successfully` and exits 0 on success, or prints
the first failure to stderr and exits 1.

It reads:

| Variable | Default |
| --- | --- |
| `TESTSYNC_HTTP_URL` | `http://localhost:9104` |
| `TESTSYNC_WS_URL` | `ws://localhost:9105` |
| `TESTSYNC_USER` | `exampleUserName` |
| `TESTSYNC_PASS` | `examplePassWord` |

The credentials must match the server's `sync_client` block, or the run fails
at the first request with a 401.

**It is not idempotent.** The test ID (`12345`) and the checkpoint identifier
(`checkpoint-1`) are hardcoded, and the flow overwrites whatever is stored for
that ID. Point `storage.sqlite_path` at a throwaway database while you work on
it, and delete that file between runs when you want a clean signal — running it
against a database you care about will clobber test run 12345.

## The Nightwatch example

`usage/` also contains a Nightwatch example. It needs a local browser and a
matching driver:

```bash
cd usage
npm install
npx nightwatch
```

Be aware that `chromedriver` and `geckodriver` are pinned to early-2024
versions, so on a current browser you will likely need to bump the pin locally
before it runs. If you touch this example, keep it honest about the protocol —
it doubles as documentation for how an agent is expected to behave, including
closing the WebSocket cleanly so the test process can exit.

## Commits and pull requests

- Match the existing history: short, imperative subject lines describing the
  change ("Add E2E test", "Rework the WS connection and checkpoint logic").
  There is no required prefix or scope convention.
- One logical change per pull request. Keep protocol changes, refactors and
  formatting churn apart — a diff that mixes them is hard to review and harder
  to revert.
- In the description, cover: what changed, why, and how you verified it. For a
  bug fix, include the failing-test evidence described above. For a concurrency
  change, include the `-count=20` result.
- Never commit your `config/` directory, a `*.db` database file, or logs.
- Update `README.md` in the same pull request when you change configuration,
  routes or the wire format. Documentation that lags the code is a bug here.

## Licensing

TestSync is licensed under the Apache License 2.0. By contributing, you agree
that your contribution is licensed under the same terms. There is no CLA.

## Security issues

Please do not open a public issue for a vulnerability. See
[SECURITY.md](SECURITY.md) for how to report one privately.
