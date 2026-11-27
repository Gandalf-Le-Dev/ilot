package daemon

import (
	"context"
	"errors"
	"net"
	"time"
)

const loopbackName = "localhost"

var loopbackDialer = net.Dialer{Timeout: 5 * time.Second}

// dialLoopback reaches localhost with no name lookup, because a resolver or a hosts file can send that name to another machine.
// It tries both addresses, because some dev servers listen on ::1 only and others on 127.0.0.1 only.
func dialLoopback(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != loopbackName {
		return loopbackDialer.DialContext(ctx, network, address)
	}
	var failures []error
	for _, ip := range []string{"::1", "127.0.0.1"} {
		connection, err := loopbackDialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
		if err == nil {
			return connection, nil
		}
		failures = append(failures, err)
	}
	return nil, errors.Join(failures...)
}
