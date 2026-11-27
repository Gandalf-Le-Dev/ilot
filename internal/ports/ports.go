package ports

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"time"
)

type Range struct {
	First int
	Last  int
}

func (portRange Range) Allocate(count int, portsHeldByStacks map[int]bool, isFreeOnHost func(port int) bool) ([]int, error) {
	allocated := make([]int, 0, count)
	for port := portRange.First; port <= portRange.Last && len(allocated) < count; port++ {
		if portsHeldByStacks[port] || !isFreeOnHost(port) {
			continue
		}
		allocated = append(allocated, port)
	}
	if len(allocated) < count {
		return nil, fmt.Errorf("only %d free ports left in %d-%d, %d needed: remove unused stacks", len(allocated), portRange.First, portRange.Last, count)
	}
	return allocated, nil
}

var loopbackAddresses = []string{"127.0.0.1", "::1"}

const connectTimeout = 500 * time.Millisecond

// IsFreeOnLoopback tests the two loopback addresses only. A listener on every interface, as before,
// can open the firewall dialog of macOS, and an app of ilot is reached on the loopback address anyway.
func IsFreeOnLoopback(port int) bool {
	for _, address := range loopbackAddresses {
		hostPort := net.JoinHostPort(address, strconv.Itoa(port))
		// On macOS, a listener on one address succeeds while another program holds the port on every interface. A connection finds that program.
		if connection, err := net.DialTimeout("tcp", hostPort, connectTimeout); err == nil {
			connection.Close()
			return false
		}
		listener, err := net.Listen("tcp", hostPort)
		if errors.Is(err, syscall.EADDRINUSE) {
			return false
		}
		if err != nil {
			// A machine with no IPv6 has no ::1, and nothing can hold a port there.
			if address == loopbackAddresses[0] {
				return false
			}
			continue
		}
		if listener.Close() != nil {
			return false
		}
	}
	return true
}
