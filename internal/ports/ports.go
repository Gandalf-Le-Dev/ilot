package ports

import (
	"fmt"
	"net"
	"strconv"
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

func IsFreeOnHost(port int) bool {
	listener, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	return listener.Close() == nil
}
