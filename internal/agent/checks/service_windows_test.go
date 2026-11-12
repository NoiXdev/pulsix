//go:build windows

package checks

import (
	"context"
	"testing"
)

func TestSCMLister(t *testing.T) {
	units := []string{"EventLog", "NoSuchSvc12345", "eventlog"}
	res, err := newUnitLister().States(context.Background(), units)
	if err != nil {
		t.Fatalf("States: %v", err)
	}
	for unit, want := range map[string]string{"EventLog": "active", "NoSuchSvc12345": "unknown", "eventlog": "active"} {
		r, ok := res[unit]
		if !ok || r.Err != nil || r.Label != want {
			t.Errorf("%s = %+v (present %v), want %s", unit, r, ok, want)
		}
	}
}
