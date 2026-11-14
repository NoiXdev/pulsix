package snmp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// TestDialDeviceChecksTheGuardAtDialTime: dialDevice's socket runs the
// guard's check too, so a target that slipped past the up-front check is
// still refused.
func TestDialDeviceChecksTheGuardAtDialTime(t *testing.T) {
	c, err := credentialsFor("s", "v2c", map[string]string{"community": "c"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = dialDevice(context.Background(), probe.NewGuard(nil, nil), netip.MustParseAddrPort("127.0.0.1:161"), time.Second, c)
	if !errors.Is(err, probe.ErrNotAllowed) || classify(err) != failRefused {
		t.Fatalf("dial loopback with the production guard: %v", err)
	}
}

// TestDialDeviceClosesTheSocketOnCancel: cancelling the session's context
// closes its socket, so a read gosnmp started after its own context check
// ends at once instead of after the request timeout.
func TestDialDeviceClosesTheSocketOnCancel(t *testing.T) {
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	c, err := credentialsFor("s", "v2c", map[string]string{"community": "c"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cl, err := dialDevice(ctx, probetest.LoopbackGuard(), netip.MustParseAddrPort(ln.LocalAddr().String()), time.Minute, c)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.close()
	conn := cl.(*gosnmpClient).conn
	done := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 16))
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("read after cancel: %v, want the socket closed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read still blocked 5 s after cancel")
	}
}
