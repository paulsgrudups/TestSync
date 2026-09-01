package wsutil

// Closed reports whether the connection has been released. It is the read-only
// counterpart of Close and never blocks. A run drops a connection when its
// reader exits, but the writer can finish a moment earlier, so a monitor uses
// this to avoid reporting a connection that is on its way out as healthy.
//
// A nil client is closed: there is nothing left to write to.
func (c *Client) Closed() bool {
	if c == nil {
		return true
	}

	select {
	case <-c.done:
		return true
	default:
		return false
	}
}
