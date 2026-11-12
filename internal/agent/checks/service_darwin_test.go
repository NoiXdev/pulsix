//go:build darwin

package checks

import (
	"context"
	"testing"
)

func TestLaunchctlListerUnknownLabel(t *testing.T) {
	res, err := newUnitLister().States(context.Background(), []string{"dev.noix.does-not-exist"})
	if err != nil {
		t.Fatalf("States: %v", err)
	}
	r := res["dev.noix.does-not-exist"]
	if r.Err != nil || r.Label != "unknown" {
		t.Errorf("result = %+v, want unknown", r)
	}
}
