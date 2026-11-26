package hosts_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
)

// TestHostAddressIsAnIPOrAHostName (decision D7, controller's call): a
// host's address reaches Ansible as ansible_host, so every writer may set
// only an IP address or a host name (the jump host address rule), or
// leave it empty (the inventory then dials the host's name).
func TestHostAddressIsAnIPOrAHostName(t *testing.T) {
	svc := hosts.NewService(newFakeHostStore(), time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, err := svc.CreateZone(ctx, org, "fra", "Falkenstein")
	if err != nil {
		t.Fatal(err)
	}
	good := map[string]string{"ipv4": "10.0.0.1", "ipv6": "2001:db8::1", "name": "web-01.example.com", "empty": ""}
	bad := map[string]string{
		"template": "{{ lookup('pipe','id') }}", "space": "web 01", "jinja block": "{% if 1 %}x{% endif %}",
		"leading dash": "-oProxyCommand=x", "url": "ssh://web-01", "newline": "web\n01",
	}
	n := 0
	for name, addr := range good {
		n++
		h, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "ok-" + string(rune('a'+n)), Address: addr, ZoneID: zone.ID, Managed: true})
		if err != nil {
			t.Fatalf("%s: create = %v", name, err)
		}
		if _, err := svc.UpdateHost(ctx, org, h.ID, h.Revision, hosts.HostInput{Name: h.Name, Address: addr, ZoneID: zone.ID, Managed: true}); err != nil {
			t.Fatalf("%s: update = %v", name, err)
		}
	}
	h, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "target", Address: "10.0.0.9", ZoneID: zone.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	for name, addr := range bad {
		if _, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "bad", Address: addr, ZoneID: zone.ID, Managed: true}); !errors.Is(err, hosts.ErrInvalidInput) {
			t.Errorf("%s: create = %v, want ErrInvalidInput", name, err)
		}
		if _, err := svc.UpdateHost(ctx, org, h.ID, h.Revision, hosts.HostInput{Name: h.Name, Address: addr, ZoneID: zone.ID, Managed: true}); !errors.Is(err, hosts.ErrInvalidInput) {
			t.Errorf("%s: update = %v, want ErrInvalidInput", name, err)
		}
	}
}
