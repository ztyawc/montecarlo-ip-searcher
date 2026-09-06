package probe

import (
	"context"
	"net"
	"sync"
)

type candidateConnectionsKey struct{}

// candidateConnections owns the sockets opened during one candidate's rounds.
// CloseIdleConnections alone cannot release an HTTP/2 connection whose canceled
// stream is still being removed asynchronously by net/http.
type candidateConnections struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	conns  []net.Conn
}

func withCandidateConnections(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	connections := &candidateConnections{ctx: ctx, cancel: cancel}
	return context.WithValue(ctx, candidateConnectionsKey{}, connections), connections.close
}

func trackCandidateConnections(dial DialContextFunc) DialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		connections, _ := ctx.Value(candidateConnectionsKey{}).(*candidateConnections)
		if connections == nil {
			return dial(ctx, network, address)
		}
		if err := connections.ctx.Err(); err != nil {
			return nil, err
		}
		// Transport detaches a dial from the request's cancellation so it may
		// serve a later request. Here it must not outlive this candidate.
		dialCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(connections.ctx, cancel)
		defer func() {
			stop()
			cancel()
		}()
		conn, err := dial(dialCtx, network, address)
		if err != nil {
			return nil, err
		}
		connections.mu.Lock()
		if connections.closed {
			connections.mu.Unlock()
			// A custom dialer can still return a connection after cancellation.
			_ = conn.Close()
			return nil, net.ErrClosed
		}
		connections.conns = append(connections.conns, conn)
		connections.mu.Unlock()
		return conn, nil
	}
}

func (c *candidateConnections) close() {
	c.mu.Lock()
	c.closed = true
	conns := c.conns
	c.conns = nil
	c.mu.Unlock()
	c.cancel()
	for _, conn := range conns {
		_ = conn.Close()
	}
}
