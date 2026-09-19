package ws

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/paulsgrudups/testsync/internal/app"
	"github.com/paulsgrudups/testsync/utils"
)

// timing matches the three clock fields of a release, which differ on every
// run. They are replaced by placeholders before a release is compared, and
// checked separately by checkReleaseTiming.
var timing = regexp.MustCompile(
	`"start_in_ms":(\d+),"server_time_ms":(\d+),"start_at":(\d+)`,
)

const timingPlaceholder = `"start_in_ms":N,"server_time_ms":N,"start_at":N`

// exchange sends one raw frame and returns the next frame the server sends.
func exchange(t *testing.T, conn *websocket.Conn, frame string) string {
	t.Helper()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("failed to send %s: %v", frame, err)
	}

	return nextFrame(t, conn)
}

// nextFrame reads one frame and asserts it is text: protocol v1 never sends a
// binary frame.
func nextFrame(t *testing.T, conn *websocket.Conn) string {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}

	kind, body, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected a reply: %v", err)
	}

	if kind != websocket.TextMessage {
		t.Fatalf("expected a text frame, got type %d: %q", kind, body)
	}

	return string(body)
}

// checkReleaseTiming asserts the clock fields of a release are consistent
// with each other and with the lead time, and returns the release with them
// replaced by placeholders.
func checkReleaseTiming(t *testing.T, reply string) string {
	t.Helper()

	match := timing.FindStringSubmatch(reply)
	if match == nil {
		t.Fatalf("release is missing its clock fields: %s", reply)
	}

	startIn, _ := strconv.ParseInt(match[1], 10, 64)
	serverTime, _ := strconv.ParseInt(match[2], 10, 64)
	startAt, _ := strconv.ParseInt(match[3], 10, 64)

	if startIn < 0 || startIn > utils.DefaultReleaseLeadTime.Milliseconds() {
		t.Fatalf("start_in_ms %d is outside [0, %s]", startIn, utils.DefaultReleaseLeadTime)
	}

	// Each field is truncated to whole milliseconds on its own, so the
	// relative and absolute forms may disagree by one.
	if diff := startAt - serverTime - startIn; diff < -1 || diff > 1 {
		t.Fatalf("start_at - server_time_ms = %d, but start_in_ms = %d",
			startAt-serverTime, startIn)
	}

	if drift := time.Since(time.UnixMilli(serverTime)); drift < -time.Second || drift > 5*time.Second {
		t.Fatalf("server_time_ms is %s away from now", drift)
	}

	return timing.ReplaceAllString(reply, timingPlaceholder)
}

// TestProtocolRepliesAreExact pins every reply of protocol v1 byte for byte
// (API-2). A change that fails this test is a change to the wire format, and
// must bump the protocol version rather than edit the expectation.
func TestProtocolRepliesAreExact(t *testing.T) {
	t.Parallel()

	type step struct {
		send string
		want string
	}

	cases := map[string]struct {
		// seed is stored as the run's payload before the steps run.
		seed  []byte
		steps []step
	}{
		"update then read, with ids echoed": {steps: []step{
			{
				`{"command":"update_data","id":"a1","content":{"k":"v"}}`,
				`{"command":"update_data","id":"a1","content":{"bytes":9}}`,
			},
			{
				`{"command":"read_data","id":7}`,
				`{"command":"read_data","id":7,"content":{"k":"v"}}`,
			},
		}},
		"no id, no id echoed": {steps: []step{
			{
				`{"command":"get_connection_count"}`,
				`{"command":"get_connection_count","content":{"count":1}}`,
			},
		}},
		"read with nothing stored": {steps: []step{
			{
				`{"command":"read_data","id":"r"}`,
				`{"command":"read_data","id":"r","content":null}`,
			},
		}},
		"read a payload that is not JSON": {
			seed: []byte{0x00, 0xff, 0x10},
			steps: []step{{
				`{"command":"read_data","id":"r"}`,
				`{"command":"error","id":"r","content":{"code":"data_not_json",` +
					`"command":"read_data","error":"the stored payload is not JSON; ` +
					`read it with GET /tests/{testID} instead"}}`,
			}},
		},
		"update without content": {steps: []step{
			{
				`{"command":"update_data","id":1}`,
				`{"command":"error","id":1,"content":{"code":"invalid_argument",` +
					`"command":"update_data","error":"update_data needs content: the payload to store"}}`,
			},
		}},
		"unknown command": {steps: []step{
			{
				`{"command":"frobnicate","id":"x"}`,
				`{"command":"error","id":"x","content":{"code":"unknown_command",` +
					`"command":"frobnicate","error":"unknown command \"frobnicate\""}}`,
			},
		}},
		"not JSON at all": {steps: []step{
			{
				`{"command":`,
				`{"command":"error","content":{"code":"invalid_message",` +
					`"command":"","error":"the message is not a JSON envelope"}}`,
			},
		}},
		"an id that is an object": {steps: []step{
			{
				`{"command":"read_data","id":{"n":1}}`,
				`{"command":"error","content":{"code":"invalid_message","command":"read_data",` +
					`"error":"id must be a JSON string or number of at most 128 bytes"}}`,
			},
		}},
		"an id that is too long": {steps: []step{
			{
				`{"command":"read_data","id":"` + strings.Repeat("i", maxRequestIDBytes) + `"}`,
				`{"command":"error","content":{"code":"invalid_message","command":"read_data",` +
					`"error":"id must be a JSON string or number of at most 128 bytes"}}`,
			},
		}},
		"a checkpoint without a target": {steps: []step{
			{
				`{"command":"wait_checkpoint","id":"w","content":{"identifier":"gate"}}`,
				`{"command":"error","id":"w","content":{"code":"invalid_argument",` +
					`"command":"wait_checkpoint",` +
					`"error":"checkpoint \"gate\" target_count must be at least 1, got 0"}}`,
			},
		}},
		"a checkpoint that completes": {steps: []step{
			{
				`{"command":"wait_checkpoint","id":"w","content":{"identifier":"solo","target_count":1}}`,
				`{"command":"wait_checkpoint","id":"w","content":{"identifier":"solo",` +
					`"finished":true,"reason":"complete","note":"","generation":1,` +
					`"joined":1,"target":1,` + timingPlaceholder + `}}`,
			},
		}},
	}

	server, application := newIntegrationServer(t)

	testID := 900

	for name, tc := range cases {
		testID++
		testID := testID

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if tc.seed != nil {
				if err := application.Service.UpdateTestData(t.Context(), testID, tc.seed); err != nil {
					t.Fatalf("failed to seed: %v", err)
				}
			}

			conn := dialRaw(t, server, fmt.Sprintf("/register/%d", testID))

			for _, s := range tc.steps {
				got := exchange(t, conn, s.send)
				if strings.Contains(s.want, timingPlaceholder) {
					got = checkReleaseTiming(t, got)
				}

				if got != s.want {
					t.Fatalf("sent %s\n got: %s\nwant: %s", s.send, got, s.want)
				}
			}
		})
	}
}

// TestProtocolLimitRejectionIsExact pins the error reply for a limit, which
// shares the envelope of every other failure.
func TestProtocolLimitRejectionIsExact(t *testing.T) {
	t.Parallel()

	server, _ := newLimitedServer(t, utils.LimitsConfig{MaxDataBytes: 8})

	conn := dialRaw(t, server, "/register/1")

	got := exchange(t, conn, `{"command":"update_data","id":"big","content":"0123456789"}`)

	want := `{"command":"error","id":"big","content":{"code":"payload_too_large",` +
		`"command":"update_data","error":"could not store data: ` +
		`test data too large: 12 bytes exceeds the 8 byte limit"}}`

	if got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
}

// TestProtocolOperatorReleaseIsExact pins the release an operator's forced
// release sends: the reason is the fixed value an agent can switch on, and the
// operator's words travel beside it in note.
func TestProtocolOperatorReleaseIsExact(t *testing.T) {
	t.Parallel()

	server, application := newIntegrationServer(t)

	conn := dialRaw(t, server, "/register/950")

	if err := conn.WriteMessage(websocket.TextMessage, []byte(
		`{"command":"wait_checkpoint","id":"j1","content":{"identifier":"gate","target_count":2}}`,
	)); err != nil {
		t.Fatalf("failed to join: %v", err)
	}

	waitForRound(t, application, 950, "gate")

	if _, err := application.Service.ReleaseCheckpoint(950, "gate", "box died"); err != nil {
		t.Fatalf("failed to release: %v", err)
	}

	got := checkReleaseTiming(t, nextFrame(t, conn))

	want := `{"command":"wait_checkpoint","id":"j1","content":{"identifier":"gate",` +
		`"finished":false,"reason":"operator_released","note":"box died",` +
		`"generation":1,"joined":1,"target":2,` + timingPlaceholder + `}}`

	if got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
}

// waitForRound polls until a checkpoint has a round in progress.
func waitForRound(t *testing.T, a *app.App, testID int, identifier string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for {
		if run, ok := a.Registry.Get(testID); ok {
			for _, cp := range run.State(testID).Checkpoints {
				if cp.Identifier == identifier && cp.Waiting {
					return
				}
			}
		}

		if time.Now().After(deadline) {
			t.Fatalf("checkpoint %q never had a round in progress", identifier)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// TestProtocolSubprotocolIsNegotiated covers the version handshake: a client
// that asks for testsync.v1 has it confirmed, and one that asks for nothing is
// still served.
func TestProtocolSubprotocolIsNegotiated(t *testing.T) {
	t.Parallel()

	server, _ := newIntegrationServer(t)
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/register/960"

	for _, offered := range [][]string{{Subprotocol}, nil} {
		dialer := websocket.Dialer{Subprotocols: offered}

		conn, resp, err := dialer.Dial(url, nil)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			t.Fatalf("offering %v: failed to dial: %v", offered, err)
		}

		want := ""
		if offered != nil {
			want = Subprotocol
		}

		if got := conn.Subprotocol(); got != want {
			t.Fatalf("offering %v: negotiated %q, want %q", offered, got, want)
		}

		if got := exchange(t, conn, `{"command":"get_connection_count"}`); !strings.HasPrefix(
			got, `{"command":"get_connection_count"`,
		) {
			t.Fatalf("offering %v: unexpected reply %s", offered, got)
		}

		_ = conn.Close()
	}
}

// TestSkewedClocksStillSynchronize is the API-3 regression test. Two agents
// whose clocks disagree by a minute both wait start_in_ms from receipt, and
// resume within a few milliseconds of each other in real time. Scheduling on
// the absolute start_at instead, as agents used to, puts the skewed one a
// minute out.
func TestSkewedClocksStillSynchronize(t *testing.T) {
	t.Parallel()

	const skew = 60 * time.Second

	server, _ := newIntegrationServer(t)

	type resume struct {
		relative time.Time // real time, scheduling on start_in_ms
		absolute time.Time // real time, scheduling on start_at
		intended time.Time // the instant the server meant, on its own clock
	}

	results := make(chan resume, 2)

	conns := []*websocket.Conn{
		dialRaw(t, server, "/register/970"),
		dialRaw(t, server, "/register/970"),
	}

	for i, conn := range conns {
		offset := time.Duration(i) * skew // agent 1's clock runs a minute fast

		go func() {
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return
			}

			_, body, err := conn.ReadMessage()
			if err != nil {
				return
			}

			received := time.Now()
			localNow := received.Add(offset)

			var m struct {
				Content struct {
					StartInMS int64 `json:"start_in_ms"`
					StartAt   int64 `json:"start_at"`
				} `json:"content"`
			}
			if json.Unmarshal(body, &m) != nil {
				return
			}

			// The deprecated way: sleep until start_at on the local clock.
			absoluteLocal := time.UnixMilli(m.Content.StartAt)
			absoluteReal := received.Add(absoluteLocal.Sub(localNow))

			results <- resume{
				relative: received.Add(time.Duration(m.Content.StartInMS) * time.Millisecond),
				absolute: absoluteReal,
				intended: time.UnixMilli(m.Content.StartAt),
			}
		}()
	}

	for _, conn := range conns {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(
			`{"command":"wait_checkpoint","content":{"identifier":"skew","target_count":2}}`,
		)); err != nil {
			t.Fatalf("failed to join: %v", err)
		}
	}

	var got []resume

	for range conns {
		select {
		case r := <-results:
			got = append(got, r)
		case <-time.After(5 * time.Second):
			t.Fatal("an agent was never released")
		}
	}

	// The server shares this process's clock, so start_at is the true resume
	// instant here. Both agents must land on it, not merely on each other: an
	// agent that ignored the delay would agree with its peer and still be
	// half a second early.
	for i, r := range got {
		if miss := r.relative.Sub(r.intended).Abs(); miss > 50*time.Millisecond {
			t.Fatalf("agent %d on start_in_ms resumed %s away from the intended instant", i, miss)
		}
	}

	// Not a requirement of the server: a demonstration that the field this
	// test exists for is the one that matters.
	if spread := got[0].absolute.Sub(got[1].absolute).Abs(); spread < skew-time.Second {
		t.Fatalf("expected start_at to be %s out for the skewed agent, got %s", skew, spread)
	}
}

// TestRepeatedJoinIsAnsweredOnItsLatestID covers the one command that can go
// without a reply of its own: a connection joining a round it is already in is
// still one agent, and its single release answers the later join, which is
// the one a retrying client is waiting on.
func TestRepeatedJoinIsAnsweredOnItsLatestID(t *testing.T) {
	t.Parallel()

	server, application := newIntegrationServer(t)

	retrier := dialRaw(t, server, "/register/980")
	peer := dialRaw(t, server, "/register/980")

	for _, id := range []string{`"first"`, `"second"`} {
		if err := retrier.WriteMessage(websocket.TextMessage, []byte(
			`{"command":"wait_checkpoint","id":`+id+
				`,"content":{"identifier":"gate","target_count":2}}`,
		)); err != nil {
			t.Fatalf("failed to join: %v", err)
		}
	}

	waitForRound(t, application, 980, "gate")

	// A query on the same connection is answered after both joins have been
	// processed, since one connection's commands are handled in order.
	if got := exchange(t, retrier, `{"command":"get_connection_count"}`); !strings.HasPrefix(
		got, `{"command":"get_connection_count"`,
	) {
		t.Fatalf("the repeated join was answered on its own: %s", got)
	}

	if err := peer.WriteMessage(websocket.TextMessage, []byte(
		`{"command":"wait_checkpoint","content":{"identifier":"gate","target_count":2}}`,
	)); err != nil {
		t.Fatalf("failed to join: %v", err)
	}

	if got := nextFrame(t, retrier); !strings.HasPrefix(
		got, `{"command":"wait_checkpoint","id":"second","content":{"identifier":"gate","finished":true`,
	) {
		t.Fatalf("unexpected release: %s", got)
	}
}

// TestCloseCommandAnswersEarlierCommandsFirst covers close: the replies to the
// commands sent before it are delivered, and then the connection closes with
// a normal closure, not a dropped socket the agent cannot tell from a crash.
func TestCloseCommandAnswersEarlierCommandsFirst(t *testing.T) {
	t.Parallel()

	server, _ := newIntegrationServer(t)

	conn := dialRaw(t, server, "/register/990")

	for _, frame := range []string{
		`{"command":"get_connection_count","id":1}`,
		`{"command":"close"}`,
	} {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("failed to send %s: %v", frame, err)
		}
	}

	if got, want := nextFrame(t, conn),
		`{"command":"get_connection_count","id":1,"content":{"count":1}}`; got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}

	_, _, err := conn.ReadMessage()

	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseNormalClosure {
		t.Fatalf("expected a normal closure, got %v", err)
	}
}

// TestCommandsAreCounted covers testsync_websocket_commands_total: a command is
// counted under its name and outcome, and a name the server does not know is
// counted as "unknown" rather than under whatever the client sent.
func TestCommandsAreCounted(t *testing.T) {
	t.Parallel()

	server, application := newIntegrationServer(t)

	conn := dialRaw(t, server, "/register/995")

	exchange(t, conn, `{"command":"get_connection_count"}`)
	exchange(t, conn, `{"command":"x-made-up-1"}`)
	exchange(t, conn, `{"command":"x-made-up-2"}`)

	counter := application.Metrics.Commands

	if got := counter.Value(CommandGetConnectionCount, "ok"); got != 1 {
		t.Fatalf("expected one successful get_connection_count, got %d", got)
	}

	if got := counter.Value("unknown", CodeUnknownCommand); got != 2 {
		t.Fatalf("expected two unknown commands, got %d", got)
	}

	if got := counter.Value("x-made-up-1", CodeUnknownCommand); got != 0 {
		t.Fatal("a client-chosen command name became a label")
	}
}
