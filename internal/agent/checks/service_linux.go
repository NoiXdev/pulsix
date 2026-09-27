//go:build linux

package checks

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

func newSystemdLister() unitLister { return dbusLister{} }

func (dbusLister) ListUnits(ctx context.Context, units []string) ([]unitState, error) {
	conn, err := dbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to systemd: %w", err)
	}
	defer conn.Close()

	statuses, err := conn.ListUnitsByNamesContext(ctx, units)
	if err != nil {
		return nil, fmt.Errorf("list units: %w", err)
	}

	out := make([]unitState, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, unitState{Name: s.Name, LoadState: s.LoadState, ActiveState: s.ActiveState})
	}
	return out, nil
}
