// Package wsutil provides utilities for WebSocket clients and messages.
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

	// PongWait is how long a connection may stay silent before its reader
	// declares the peer gone. It is derived from pingPeriod rather than chosen
	// independently: a peer gets three pings, so a single lost ping does not
	// reap a healthy agent, while a vanished one is released within 30s
	// instead of holding its goroutine, descriptor and barrier slot forever.
	PongWait = 3 * pingPeriod

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
	// finished is closed by WritePump on its way out, once any close frame has
	// been written and the socket released. Shutdown waits on it so that a
	// close frame is not lost to the process exiting first.
	finished chan struct{}

	// closeCode and closeReason are the close frame WritePump sends as it
	// exits. They are written inside closeOnce and read only after done is
	// closed, so closing the channel is the happens-before edge that makes
	// them safe to read without a lock.
	closeCode   int
	closeReason string

	closeOnce sync.Once
}

// NewClient wraps a connection. The caller must start WritePump exactly once,
// in its own goroutine, before any message is sent.
func NewClient(conn *websocket.Conn) *Client {
	return &Client{
		conn:     conn,
		out:      make(chan outbound, outboundBuffer),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
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

// Close releases the connection without sending a close frame. It is safe to
// call repeatedly and from any goroutine; the connection itself is closed by
// WritePump on its way out.
func (c *Client) Close() {
	if c == nil {
		return
	}

	c.closeOnce.Do(func() { close(c.done) })
}

// CloseWithReason releases the connection after telling the peer why, using a
// WebSocket close code such as [websocket.CloseServiceRestart] (1012) for a
// restart or [websocket.CloseTryAgainLater] (1013) for a limit that the agent
// may retry against. An agent that is told why can distinguish a deploy or a
// rejection from a crash, which a dropped socket looks like.
//
// Like Close it is safe to call repeatedly and from any goroutine; the first
// call decides the code.
func (c *Client) CloseWithReason(code int, reason string) {
	if c == nil {
		return
	}

	c.closeOnce.Do(func() {
		c.closeCode = code
		c.closeReason = reason

		close(c.done)
	})
}

// Finished returns a channel that is closed once the connection's writer has
// stopped, which is after any close frame has been written.
func (c *Client) Finished() <-chan struct{} {
	if c == nil {
		closed := make(chan struct{})
		close(closed)

		return closed
	}

	return c.finished
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
		c.writeCloseFrame()

		if err := c.conn.Close(); err != nil {
			log.Debugf("failed to close underlying websocket connection: %v", err)
		}

		close(c.finished)
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

// writeCloseFrame tells the peer why the connection is going away. It runs on
// the writer's way out, so it is still the only goroutine writing to the
// connection. A client closed without a reason gets no frame, which is the
// behaviour every existing caller of Close relies on.
func (c *Client) writeCloseFrame() {
	if c.closeCode == 0 {
		return
	}

	err := c.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(c.closeCode, c.closeReason),
		time.Now().Add(writeWait),
	)
	if err != nil {
		log.Debugf("Could not send WS close frame: %s", err.Error())
	}
}

func (c *Client) write(messageType int, data []byte) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}

	return c.conn.WriteMessage(messageType, data)
}
