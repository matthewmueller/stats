package netstats

import (
	"io"
	"math"
	"net"
	"sync"
	"time"

	"github.com/segmentio/stats"
)

func init() {
	stats.DefaultEngine.SetHistogramBuckets("conn.read.bytes",
		1e2, // 100 B
		1e3, // 1 KB
		1e4, // 10 KB
		1e5, // 100 KB
		math.Inf(+1),
	)

	stats.DefaultEngine.SetHistogramBuckets("conn.write.bytes",
		1e2, // 100 B
		1e3, // 1 KB
		1e4, // 10 KB
		1e5, // 100 KB
		math.Inf(+1),
	)
}

// NewConn returns a net.Conn object that wraps c and produces metrics on the
// default engine.
func NewConn(c net.Conn) net.Conn {
	return NewConnWith(stats.DefaultEngine, c)
}

// NewConn returns a net.Conn object that wraps c and produces metrics on eng.
func NewConnWith(eng *stats.Engine, c net.Conn) net.Conn {
	nc := &conn{
		Conn:  c,
		eng:   eng,
		proto: c.LocalAddr().Network(),
	}
	eng.Incr("conn.open.count", stats.Tag{"protocol", nc.proto})
	return nc
}

type conn struct {
	net.Conn
	eng   *stats.Engine
	proto string
	once  sync.Once
}

func (c *conn) BaseConn() net.Conn {
	return c.Conn
}

func (c *conn) Close() (err error) {
	err = c.Conn.Close()
	c.once.Do(func() {
		if err != nil {
			c.error("close", err)
		}
		c.eng.Incr("conn.close.count", stats.Tag{"protocol", c.proto})
	})
	return
}

func (c *conn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)

	if n >= 0 {
		c.eng.Observe("conn.read.bytes", float64(n), stats.Tag{"protocol", c.proto})
	}

	if err != nil && err != io.EOF {
		c.error("read", err)
	}

	return
}

func (c *conn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)

	if n >= 0 {
		c.eng.Observe("conn.write.bytes", float64(n), stats.Tag{"protocol", c.proto})
	}

	if err != nil {
		c.error("write", err)
	}

	return
}

func (c *conn) SetDeadline(t time.Time) (err error) {
	if err = c.Conn.SetDeadline(t); err != nil {
		c.error("set-deadline", err)
	}
	return
}

func (c *conn) SetReadDeadline(t time.Time) (err error) {
	if err = c.Conn.SetReadDeadline(t); err != nil {
		c.error("set-read-deadline", err)
	}
	return
}

func (c *conn) SetWriteDeadline(t time.Time) (err error) {
	if err = c.Conn.SetWriteDeadline(t); err != nil {
		c.error("set-write-deadline", err)
	}
	return
}

func (c *conn) error(op string, err error) {
	switch err = rootError(err); err {
	case io.EOF, io.ErrClosedPipe, io.ErrUnexpectedEOF:
		// this is expected to happen when connections are closed
	default:
		// only report serious errors, others should be handled gracefully
		if !isTemporary(err) {
			c.eng.Incr("conn.error.count", stats.Tag{"protocol", c.proto}, stats.Tag{"operation", op})
		}
	}
}

func rootError(err error) error {
searchRootError:
	for i := 0; i != 10; i++ { // protect against cyclic errors
		switch e := err.(type) {
		case *net.OpError:
			err = e.Err
		default:
			break searchRootError
		}
	}
	return err
}
