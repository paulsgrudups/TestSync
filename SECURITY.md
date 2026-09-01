# Security Policy

TestSync is a synchronization server for parallel test agents. It exposes an
HTTP API for per-test data and a WebSocket API for checkpoint barriers, both
behind the same credentials. Anyone who can reach those ports and authenticate
can read, overwrite and release the data of every test run on the server.

Please read [Current posture](#current-posture) before reporting: this project
is pre-1.0 and some weaknesses are known and deliberate rather than new.

## Supported versions

| Version | Supported |
| --- | --- |
| `master` (latest commit) | Yes |
| Anything older | No |

There are no tagged releases yet. Fixes land on the default branch, and the
only supported thing to run is its latest commit. When releases start, this
table will say which of them still receive fixes.

## Reporting a vulnerability

**Report privately through GitHub, not in a public issue or pull request.**

Open a private vulnerability advisory:

<https://github.com/paulsgrudups/TestSync/security/advisories/new>

(From the repository: **Security** → **Advisories** → **Report a
vulnerability**.) The report is visible only to you and the maintainers until a
fix is published.

Please include, as far as you can:

- What an attacker gains — read another run's data, release a barrier they do
  not participate in, bypass authentication, crash or hang the server, and so
  on.
- The commit you tested, your Go version and your OS.
- The relevant parts of `configuration.json` with credentials removed —
  especially `auth.mode` and the storage settings.
- Reproduction steps. A small script or `go test` against a locally built
  server is the fastest possible report to act on.
- Whether the server was running with authentication enabled or with
  `auth.mode: "none"` / `--insecure-no-auth`.

Please do not include real credentials, customer data, or anything from a
system you do not own.

## What to expect

This is a small project maintained in spare time, so these are honest
intentions rather than a contractual SLA:

- **Acknowledgement:** within about 5 working days.
- **Initial assessment** — whether it reproduces, and how serious it looks:
  within about 10 working days.
- **Progress:** an update at least every two weeks while the report is open.
- **Disclosure:** coordinated. The fix lands first, then the advisory is
  published, and you are credited by whatever name you ask for unless you would
  rather stay anonymous.

If a report does not get a reply in a reasonable time, please ping the advisory
thread before disclosing publicly.

There is no bug bounty and no monetary reward.

## Scope

In scope, on the latest commit of the default branch:

- Authentication bypass on any HTTP or WebSocket route that is meant to require
  credentials.
- Cross-run access: reading, modifying or deleting another test run's data, or
  joining and releasing a barrier belonging to a run the attacker is not part
  of, through the documented API.
- Remote crashes, deadlocks, unbounded memory or goroutine growth reachable
  from an authenticated client through the WebSocket or HTTP API.
- Data-integrity flaws in the barrier itself where the server reports
  `finished: true` and `reason: "complete"` for a round that did not actually
  synchronize the expected number of agents. A false "everyone is here" is the
  worst thing this server can do.
- Credential handling defects — leaking credentials into logs or responses,
  timing-dependent comparison, storing them where they do not belong.
- Path traversal or similar in the storage layer, or corruption of the SQLite
  database from ordinary API use.

Out of scope:

- Anything that requires the server to be started with authentication
  explicitly disabled (`auth.mode: "none"` or `--insecure-no-auth`). That mode
  is documented as unauthenticated, prints a warning banner on every startup,
  and is for local development only.
- The absence of TLS. TestSync serves plain HTTP and WebSocket today; running
  it across an untrusted network without a TLS-terminating proxy in front is a
  deployment decision, not a bug in the server.
- Attacks that require access to the machine, the config directory, or the
  SQLite database file. Anyone with that access already has the credentials.
- Denial of service by sheer traffic volume, or by configuring absurd limits.
- The examples under `usage/`, including outdated pinned browser drivers in
  `usage/package.json`. They are illustrations, not shipped software.
- Findings from an automated scanner with no demonstrated impact on TestSync.

## Current posture

TestSync began as a proof of concept and is being hardened in the open. Some
things are already deliberate, and reporting them as new findings will only get
you this section quoted back:

- **WebSocket credentials may still be passed as query parameters.** The
  `Authorization` header is the supported path, but `/register/{testID}` also
  accepts `?username=&password=` as a fallback for clients that cannot set
  headers. Query strings leak into proxy and access logs, so this is documented
  as deprecated, logs a deprecation warning on every use, and is planned for
  removal. It is a known limitation, not an oversight.
- **There is no rate limiting on authentication failures.** Nothing slows down
  or blocks a client that guesses credentials repeatedly. Credentials are
  compared as SHA-256 hashes with `crypto/subtle`, so the comparison itself is
  constant time and does not leak length, but the guessing rate is currently
  bounded only by the network. Do not expose TestSync directly to an untrusted
  network.

A concrete way to make either of those worse — a bypass, a leak into a response
or a log, a way to turn them into cross-run access — is very much in scope and
worth reporting.

Beyond that: the server has no transport encryption of its own, no
authorization model finer than "one shared credential per server", and no
audit log. These are known gaps on the road to 1.0 rather than accepted
positions, and design input on them is welcome as a normal public issue.
