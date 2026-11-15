//go:build !linux

package zfs

import (
	"context"
	"testing"
)

func TestUnsupportedPlatform(t *testing.T) {
	obs, err := New().Collect(context.Background(), nil)
	if err == nil || len(obs) != 1 {
		t.Fatalf("obs=%+v err=%v", obs, err)
	}
	o := obs[0]
	if o.Item != "" || o.Metric != "zfs.pools" || o.Err != "zfs: not supported on this platform" {
		t.Fatalf("obs = %+v", o)
	}
}
