# TestSync WebSocket protocol

This document is the specification for the WebSocket protocol that test agents
speak to a TestSync server. It describes **protocol v1**. The server's tests
check every message shape in it byte for byte, so the document and the server
cannot drift apart without a test failing.

- [Connecting](#connecting)
- [Versioning](#versioning)
- [Envelope](#envelope)
- [Commands](#commands)
- [Checkpoints](#checkpoints)
- [Errors](#errors)
- [Closing](#closing)
- [Writing a client](#writing-a-client)
- [Version history](#version-history)

## Connecting

```
GET ws://<host>:<http_port>/register/{testID}
Authorization: Basic <base64(username:password)>
Sec-WebSocket-Protocol: testsync.v1
```

- `{testID}` is a non-negative integer of at most 19 digits. Every agent of one
  test run uses the same ID. It is the same ID that the HTTP API uses, so data
  stored with `POST /tests/{testID}` can be read here, and data written here
  can be read over HTTP.
- Credentials are the server's `sync_client` credentials, sent as HTTP Basic
  auth. They are not needed when the server runs with `auth.mode: none`.
- Before the upgrade, the server can refuse a registration with an HTTP
  status: `400` or `404` for an unusable test ID, `401` for missing or wrong
  credentials, and `503` when the server already holds `limits.max_tests` runs.

The server pings every connection. A standard WebSocket client answers the
pings on its own. A client that stops answering is disconnected.

## Versioning

The protocol version is negotiated with the `Sec-WebSocket-Protocol` header.

| Client offers | Server behaviour |
| --- | --- |
| `testsync.v1` | Confirms `testsync.v1` and speaks v1 |
| No subprotocol | Speaks v1 without confirming a subprotocol |

Clients should offer `testsync.v1`. When a v2 exists, it will be served only to
clients that ask for it by name. A client that offers nothing, or offers
`testsync.v1`, will keep getting v1.

Within one version, the server may add:

- new commands,
- new error codes,
- new fields in `content`.

Clients must ignore anything they do not recognise. Renaming or removing a
field, or changing what a field means, requires a new version.

## Envelope

Every frame in both directions is a UTF-8 **text** frame that holds one JSON
object. The server never sends binary frames.

```json
{"command": "<name>", "id": "<optional>", "content": <value>}
```

| Field | Direction | Meaning |
| --- | --- | --- |
| `command` | both | The command name, or `error` on a failure reply |
| `id` | both | Optional correlation token. The client chooses it; the server echoes it unchanged on the reply to that command. A JSON string or number, at most 128 bytes as encoded. The server omits `id` when the command carried none |
| `content` | both | The command's argument or result. Its shape depends on the command. The server always sends `content`; a client may omit it when a command takes no argument |

**Every command gets exactly one reply.** The reply is either the command's
result or an [`error`](#errors). Replies come in the order the commands were
sent on that connection, with two exceptions:

- A `wait_checkpoint` reply is the release that ends its round, so it arrives
  whenever that round ends. Other replies may arrive before it.
- `close` has no reply. The server closes the connection instead.

Clients that pipeline commands can match replies to requests with `id`.

## Commands

### `update_data`

Replaces the run's stored payload with `content`. `content` may be any JSON
value except `null`, and is stored exactly as sent.

```json
→ {"command":"update_data","id":"a1","content":{"user":"alice","cart":[3,7]}}
← {"command":"update_data","id":"a1","content":{"bytes":29}}
```

`bytes` is the size of the stored payload. When the write fails, the reply is
an `error`, either `payload_too_large` or `invalid_argument`. Once the
acknowledgement arrives, the payload is stored, and any agent that reads it
afterwards will see it.

### `read_data`

Returns the run's stored payload as `content`, exactly as it was stored.

```json
→ {"command":"read_data","id":7}
← {"command":"read_data","id":7,"content":{"user":"alice","cart":[3,7]}}
```

When nothing is stored, `content` is `null`. The WebSocket protocol only
carries JSON. A payload that is not valid JSON can only have been stored over
HTTP; reading it here fails with `data_not_json`, and it has to be read with
`GET /tests/{testID}` instead.

### `get_connection_count`

Returns how many agents are connected to the run, this one included.

```json
→ {"command":"get_connection_count"}
← {"command":"get_connection_count","content":{"count":4}}
```

An agent that disconnects stops being counted within one round-trip.

### `wait_checkpoint`

Joins a checkpoint barrier. See [Checkpoints](#checkpoints).

### `close`

Asks the server to close the connection. It takes no content and gets no reply.
The server first delivers the replies to the commands sent before `close`, and
then closes the connection with code `1000`. Closing the WebSocket from
the client side works just as well.

## Checkpoints

A checkpoint is a named barrier. Each agent that joins it waits until the
current **round** of the barrier ends. Then every agent in that round is
released together and told to resume at the same moment.

### Joining

```json
→ {"command":"wait_checkpoint","id":"j1","content":{
    "identifier":"login-complete","target_count":4,"timeout_ms":60000}}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `identifier` | yes | Names the barrier. Any non-empty string |
| `target_count` | yes | How many distinct connections the round waits for. At least `1` |
| `timeout_ms` | no | Bounds the wait. Omitted or `0` means 60 s. Anything above 30 min is clamped to 30 min. Negative values are rejected |

The first agent to join a round sets its `target_count` and `timeout_ms`, and
the round's deadline counts from that agent's arrival. The other agents in the
round are expected to send the same values.

The join itself gets no reply. The reply is the release, when the round ends.
An invalid join is answered straight away with an `invalid_argument` error, and
nothing is joined.

If the same connection joins a round it is already in, it still counts as one
agent. Its release echoes the `id` of its **latest** join, because that is the
join a retrying client is waiting on.

### The release

When a round ends, every agent in it receives a release. The releases differ
only in `id`, which echoes each agent's own join, and in the two clock fields,
which are taken as each agent's release is sent.

```json
← {"command":"wait_checkpoint","id":"j1","content":{
    "identifier":"login-complete",
    "finished":true,
    "reason":"complete",
    "note":"",
    "generation":1,
    "joined":4,
    "target":4,
    "start_in_ms":498,
    "server_time_ms":1789037384226,
    "start_at":1789037384726}}
```

Every field is always present.

| Field | Meaning |
| --- | --- |
| `identifier` | The barrier that ended |
| `finished` | `true` if and only if `reason` is `complete` |
| `reason` | Why the round ended. One of the values in the table below, and nothing else |
| `note` | Free text an operator attached to a forced release; otherwise `""`. It is meant for people reading logs. Never branch on it |
| `generation` | The round's number, counted from `1` for each identifier |
| `joined` / `target` | How many agents arrived, and how many the round waited for |
| `start_in_ms` | **How long to wait, from receiving this message, before resuming** |
| `server_time_ms` | The server's clock when it sent this message, in Unix milliseconds. Use it to detect clock skew, not to schedule anything |
| `start_at` | *Deprecated.* The resume instant on the server's clock. It will be removed in v2 |

| `reason` | `finished` | Meaning |
| --- | --- | --- |
| `complete` | `true` | `target` agents arrived. The agents are synchronized |
| `timeout` | `false` | The round's deadline passed first |
| `participant_lost` | `false` | An agent disconnected, and too few remain to reach `target` |
| `operator_released` | `false` | An operator ended the round from the management API |

A release with `finished: false` still tells every agent in the round to resume
together. But the barrier was not met, so a test suite that depends on it
should fail loudly rather than carry on unsynchronized.

### Resuming

**Sleep `start_in_ms` from the moment the release is received, then resume.**

The delay is relative on purpose. CI machines' clocks commonly disagree by
seconds, and sometimes by minutes. An agent that sleeps until `start_at` on its
own clock resumes early or late by the full difference between the two clocks.
`start_in_ms` does not depend on clocks agreeing, so skew doesn't affect it.

The server computes `start_in_ms` separately for each agent, as it queues that
agent's release. All agents in a round therefore land on the same instant, give
or take the network delay. The server's lead time defaults to 500 ms and is
configured with `checkpoint.release_lead_time`.

A client can estimate its clock skew as `server_time_ms - now()` at receipt,
and warn when the skew is large. The estimate is off by the one-way network
delay.

### Reuse

When a round ends, the identifier immediately starts a new, empty round. A
looping suite can call `wait_checkpoint` with the same identifier on every
iteration, and each round blocks on its own. There is no need for per-iteration
identifiers. `generation` tells one round from the next.

### Losing an agent

If a connection drops and fewer connections remain on the run than the round's
`target`, every agent still waiting is released with `participant_lost`. The
server doesn't hold them for an agent that is never coming back.

An agent that disconnects before the others have joined cannot be detected
this way, because the server doesn't know yet that it was expected. The
round's timeout is the backstop for that case.

## Errors

A failed command is answered with an `error` reply. It carries the failed
command's `id`, if the command had one.

```json
← {"command":"error","id":"a1","content":{
    "code":"payload_too_large",
    "command":"update_data",
    "error":"could not store data: test data too large: 12 bytes exceeds the 8 byte limit"}}
```

| Field | Meaning |
| --- | --- |
| `code` | A stable identifier. Safe to branch on |
| `command` | The command that failed, or `""` when the frame could not be read far enough to tell |
| `error` | A human-readable explanation, for logs. It may change at any time. Never parse it |

| `code` | Meaning | The connection |
| --- | --- | --- |
| `invalid_message` | The frame is not a JSON envelope, or its `id` is not a string or number of at most 128 bytes. `id` is not echoed | stays open |
| `unknown_command` | `command` is not one the server knows | stays open |
| `invalid_argument` | `content` is missing or unusable, for example a checkpoint without a `target_count` | stays open |
| `data_not_json` | `read_data` found a payload that is not JSON. Read it over HTTP | stays open |
| `payload_too_large` | The payload exceeds `limits.max_data_bytes`. Nothing was stored | stays open |
| `checkpoint_limit_reached` | The run already has `limits.max_checkpoints_per_test` identifiers. Existing identifiers keep working | stays open |
| `test_limit_reached` | The server already holds `limits.max_tests` runs | stays open |
| `connection_limit_reached` | The run already has `limits.max_connections_per_test` agents | stays open |
| `internal_error` | The server failed, for example in storage. The command may be retried | stays open |

The set of codes only grows within a version, so a client must treat an
unrecognised code as a generic failure.

## Closing

| Code | Reason text | Meaning |
| --- | --- | --- |
| `1000` | *(none)* | The client sent `close` |
| `1000` | `disconnected by operator` | An operator disconnected this agent |
| `1000` | `run deleted by operator` | An operator deleted the run |
| `1009` | | A frame far larger than `limits.max_data_bytes` was rejected from its header |
| `1012` | | The server is restarting. Reconnect |
| `1013` | `too many active test runs` | `limits.max_tests` was reached during the upgrade. Retry later |
| `1013` | `connection limit reached for this test run` | `limits.max_connections_per_test` was reached. Retry later |

A connection that closes while it is waiting on a checkpoint gives up its place
in the round. That may end the round for everyone else with `participant_lost`.

## Writing a client

A minimal agent needs to do five things:

1. Connect with Basic auth, offering `testsync.v1`.
2. Give each command a unique `id`, and keep a table of pending ids.
3. Read frames in a single loop, and resolve each pending id when its reply
   arrives. An `error` reply rejects the pending id.
4. For a checkpoint: send `wait_checkpoint`, wait for the reply with that `id`,
   check `finished`, then sleep `start_in_ms`.
5. Close the socket when the test ends, including when it fails. An agent that
   stays connected holds its place in future rounds.

## Version history

| Version | Changes |
| --- | --- |
| v1 | First versioned protocol. Replaces the unversioned protocol the server spoke before. Breaking changes from it: `read_data` replies in the envelope instead of as a raw binary frame; `update_data` is acknowledged; every failed command gets an `error` reply (before, only limit rejections did); correlation `id`s; release gains `start_in_ms`, `server_time_ms` and `note`; an operator's release text moved from `reason` to `note`, so that `reason` is always one of the four fixed values |
