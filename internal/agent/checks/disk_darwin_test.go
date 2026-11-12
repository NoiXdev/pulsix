//go:build darwin

package checks

import (
	"context"
	"strings"
	"testing"
)

func TestDiskLiveDiscoveryOnThisMac(t *testing.T) {
	obs, err := newDiskPlugin().Collect(context.Background(), nil)
	if err != nil {
		t.Logf("collect error (tolerated, a mount may be unreadable): %v", err)
	}
	if len(obs) == 0 {
		t.Fatal("no observations")
	}
	for _, o := range obs {
		if o.Item != "/" && o.Item != "/System/Volumes/Data" && !strings.HasPrefix(o.Item, "/Volumes/") {
			t.Errorf("unexpected item %q", o.Item)
		}
	}
}
