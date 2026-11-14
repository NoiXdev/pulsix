package snmp

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
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
