package sshconf_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/sshconf"
)

func bastion(name, addr string, port int, via *uuid.UUID) sshconf.Bastion {
	return sshconf.Bastion{ID: uuid.Must(uuid.NewV7()), Name: name, Address: addr, Port: port, Username: "ops", ViaBastionID: via}
}

func TestChainOrdersFirstHopFirst(t *testing.T) {
	a := bastion("a", "a.example", 22, nil)
	b := bastion("b", "b.example", 2222, &a.ID)
	c := bastion("c", "2001:db8::1", 22, &b.ID)
	all := map[uuid.UUID]sshconf.Bastion{a.ID: a, b.ID: b, c.ID: c}
	hops, err := sshconf.Chain(all, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != 3 || hops[0].Name != "a" || hops[2].Name != "c" {
		t.Fatalf("hops = %v", hops)
	}
	if got := sshconf.ProxyJump(hops); got != "ops@a.example:22,ops@b.example:2222,ops@[2001:db8::1]:22" {
		t.Fatalf("ProxyJump = %q", got)
	}
	if got := sshconf.ProxyJump(nil); got != "" {
		t.Fatalf("ProxyJump(nil) = %q", got)
	}
}

func TestChainRejects(t *testing.T) {
	a := bastion("a", "a.example", 22, nil)
	b := bastion("b", "b.example", 22, &a.ID)
	a.ViaBastionID = &b.ID // a -> b -> a
	cycle := map[uuid.UUID]sshconf.Bastion{a.ID: a, b.ID: b}
	if _, err := sshconf.Chain(cycle, a.ID); !errors.Is(err, sshconf.ErrInvalidInput) || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle: %v", err)
	}
	if _, err := sshconf.Chain(map[uuid.UUID]sshconf.Bastion{}, uuid.Must(uuid.NewV7())); !errors.Is(err, sshconf.ErrInvalidInput) || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unknown: %v", err)
	}
	long := map[uuid.UUID]sshconf.Bastion{}
	var prev *uuid.UUID
	var last uuid.UUID
	for i := 0; i <= sshconf.MaxChain; i++ {
		h := bastion("h", "h.example", 22, prev)
		long[h.ID] = h
		id := h.ID
		prev, last = &id, h.ID
	}
	if _, err := sshconf.Chain(long, last); !errors.Is(err, sshconf.ErrInvalidInput) || !strings.Contains(err.Error(), "4 hops") {
		t.Fatalf("too long: %v", err)
	}
}
