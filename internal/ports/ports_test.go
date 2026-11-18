package ports

import (
	"net"
	"testing"
)

func TestAllocateSkipsPortsHeldByStacksAndPortsBusyOnTheHost(t *testing.T) {
	portRange := Range{First: 20000, Last: 20009}
	portsHeldByStacks := map[int]bool{20000: true, 20002: true}
	isFreeOnHost := func(port int) bool { return port != 20001 }

	allocated, err := portRange.Allocate(3, portsHeldByStacks, isFreeOnHost)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	want := []int{20003, 20004, 20005}
	if len(allocated) != len(want) || allocated[0] != want[0] || allocated[1] != want[1] || allocated[2] != want[2] {
		t.Fatalf("Allocate = %v, want %v", allocated, want)
	}
}

func TestAllocateFailsWhenTheRangeIsExhausted(t *testing.T) {
	portRange := Range{First: 20000, Last: 20001}
	if _, err := portRange.Allocate(3, nil, func(int) bool { return true }); err == nil {
		t.Fatal("want an error when the range holds fewer free ports than requested")
	}
}

func TestIsFreeOnHostSeesAListeningPort(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	listenerAddress, isTCP := listener.Addr().(*net.TCPAddr)
	if !isTCP {
		t.Fatalf("listener address %v is not TCP", listener.Addr())
	}
	busyPort := listenerAddress.Port

	if IsFreeOnHost(busyPort) {
		t.Errorf("IsFreeOnHost(%d) = true for a port with a listener", busyPort)
	}
}
