## What and why

<!-- What changes, and what problem it solves. Link the issue if there is one. -->

## How it was verified

<!-- Paste the relevant output. "Ran the tests" is not evidence. -->

- [ ] `go build ./...`
- [ ] `go vet ./...`
- [ ] `gofmt -l .` prints nothing
- [ ] `golangci-lint run ./...` reports 0 issues
- [ ] `go test -race ./...` is green
- [ ] Touches `api/runs`, `api/ws` or `wsutil`? `go test -race -count=20 ./api/...` is green
- [ ] Bug fix? The regression test was **verified to fail before the fix** (failure output below)

## Compatibility

- [ ] No existing WebSocket field changed name, type or meaning (new information went into new optional fields)
- [ ] `usage/e2e/main.go` and `usage/tests/connection.js` still work
- [ ] `README.md` updated if configuration, routes or the wire format changed

CI must be green before merge.
