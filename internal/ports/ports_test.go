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

func listenOn(t *testing.T, address string) (port int) {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
	if err != nil {
		t.Skipf("this machine cannot listen on %s: %v", address, err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener.Addr().(*net.TCPAddr).Port
}

func TestAPortWithAListenerOnALoopbackAddressIsNotFree(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1"} {
		t.Run(address, func(t *testing.T) {
			busyPort := listenOn(t, address)

			if IsFreeOnLoopback(busyPort) {
				t.Errorf("IsFreeOnLoopback(%d) = true for a port with a listener on %s", busyPort, address)
			}
		})
	}
}

func TestAPortWithNoListenerIsFree(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	freedPort := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	if !IsFreeOnLoopback(freedPort) {
		t.Errorf("IsFreeOnLoopback(%d) = false for a port that nothing holds", freedPort)
	}
}
