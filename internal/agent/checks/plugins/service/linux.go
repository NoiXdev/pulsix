//go:build linux

package service

import (
	"context"
	"fmt"

	"github.com/coreos/go-systemd/v22/dbus"
)

// dbusLister is the real unitLister: one systemd system-bus connection per
// Collect call, closed immediately after — check intervals are tens of
// seconds apart, so a fresh connection each time costs nothing and avoids
// keeping a stale bus session open between runs.
type dbusLister struct{}

func newUnitLister() unitLister { return dbusLister{} }

func (dbusLister) States(ctx context.Context, units []string) (map[string]unitResult, error) {
	conn, err := dbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to systemd: %w", err)
	}
	defer conn.Close()

	statuses, err := conn.ListUnitsByNamesContext(ctx, units)
	if err != nil {
		return nil, fmt.Errorf("list units: %w", err)
	}

	out := make(map[string]unitResult, len(statuses))
	for _, s := range statuses {
		out[s.Name] = unitResult{Label: systemdLabel(s.LoadState, s.ActiveState)}
	}
	return out, nil
}
