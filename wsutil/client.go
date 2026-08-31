package wsutil

import (
	stderrors "errors"
	"runtime/debug"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	log "github.com/sirupsen/logrus"
)

const (
	// writeWait bounds a single write to the underlying connection so that a
	// peer that vanished without closing its socket cannot stall the writer.
	writeWait = 10 * time.Second

	// pingPeriod is how often an idle connection is pinged.
	pingPeriod = 10 * time.Second

	// outboundBuffer is how many messages may be queued for one connection
	// before it is treated as unable to keep up.
	outboundBuffer = 64
)

var (
	// ErrClientClosed indicates the connection is no longer writable.
	ErrClientClosed = stderrors.New("websocket connection closed")

	// ErrClientBacklog indicates the connection's outbound queue is full, so
	// the connection has been closed. A client that cannot drain a checkpoint
	// release is of no use to the agents waiting on it, and must never be
	// allowed to block them.
	ErrClientBacklog = stderrors.New("websocket outbound queue is full")
)

type outbound struct {
	messageType int
	data        []byte
}

// Client owns a single WebSocket connection. gorilla/websocket supports only
// one concurrent writer per connection and panics when that is violated, so
// every write is queued here and performed by WritePump, which is the only
// goroutine that ever writes to the connection.
type Client struct {
	conn *websocket.Conn
	out  chan outbound
	done chan struct{}

	closeOnce sync.Once
}

// NewClient wraps a connection. The caller must start WritePump exactly once,
// in its own goroutine, before any message is sent.
func NewClient(conn *websocket.Conn) *Client {
	return &Client{
		conn: conn,
		out:  make(chan outbound, outboundBuffer),
		done: make(chan struct{}),
	}
}

// Conn returns the underlying connection. It may be used for reading only:
// writing to it directly races with WritePump.
func (c *Client) Conn() *websocket.Conn {
	return c.conn
}

// Send queues a message for the connection. It never blocks: a client that
// cannot keep up is closed instead of being allowed to stall the sender, which
// is frequently another agent's goroutine broadcasting a checkpoint release.
func (c *Client) Send(messageType int, data []byte) error {
	if c == nil {
		return ErrClientClosed
	}

	select {
	case c.out <- outbound{messageType: messageType, data: data}:
		return nil
	case <-c.done:
		return ErrClientClosed
	default:
		c.Close()
		return ErrClientBacklog
	}
}

// Close releases the connection. It is safe to call repeatedly and from any
// goroutine; the connection itself is closed by WritePump on its way out.
func (c *Client) Close() {
	if c == nil {
		return
	}

	c.closeOnce.Do(func() { close(c.done) })
}

// WritePump serialises all writes to the connection, including keepalive
// pings. It runs until the client is closed or a write fails, and closes the
// connection as it exits, which in turn unblocks the connection's reader.
func (c *Client) WritePump() {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf(
				"Recovered panic in WebSocket writer: %v\n%s", r, debug.Stack(),
			)
		}
	}()

	ping := time.NewTicker(pingPeriod)

	defer func() {
		ping.Stop()
		c.Close()
		c.conn.Close() // nolint: errcheck
	}()

	for {
		select {
		case msg := <-c.out:
			if err := c.write(msg.messageType, msg.data); err != nil {
				log.Debugf("Could not send WS message: %s", err.Error())
				return
			}
		case <-ping.C:
			err := c.conn.WriteControl(
				websocket.PingMessage,
				[]byte("ping"),
				time.Now().Add(writeWait),
			)
			if err != nil {
				log.Debugf("Could not send WS ping message: %s", err.Error())
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *Client) write(messageType int, data []byte) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}

	return c.conn.WriteMessage(messageType, data)
}
