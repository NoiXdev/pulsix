package raid

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

type fakeSource struct {
	data []byte
	err  error
}

func (f fakeSource) Read(context.Context) ([]byte, error) { return f.data, f.err }

func find(obs []checks.Observation, item, metric string) *checks.Observation {
	for i := range obs {
		if obs[i].Item == item && obs[i].Metric == metric {
			return &obs[i]
		}
	}
	return nil
}

func TestCollectRecovering(t *testing.T) {
	p := raidPlugin{Source: fakeSource{data: load(t, "recovering.txt")}}
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if o := find(obs, "md0", "raid.state"); o == nil || *o.Label != "recovering" {
		t.Fatalf("state = %+v", o)
	}
	if o := find(obs, "md0", "raid.sync_pct"); o == nil || *o.Value != 12.3 {
		t.Fatalf("sync_pct = %+v", o)
	}
	if o := find(obs, "md0", "raid.devices_failed"); o == nil || *o.Value != 0 {
		t.Fatalf("failed = %+v", o)
	}
	if o := find(obs, "", "raid.arrays"); o == nil || *o.Value != 1 {
		t.Fatalf("arrays = %+v", o)
	}
}

func TestCollectNoSyncPctWhenPending(t *testing.T) {
	p := raidPlugin{Source: fakeSource{data: load(t, "resync_pending.txt")}}
	obs, _ := p.Collect(context.Background(), nil)
	if find(obs, "md1", "raid.sync_pct") != nil {
		t.Fatal("sync_pct must be absent while pending")
	}
}

func TestCollectFilterAndDedupe(t *testing.T) {
	p := raidPlugin{Source: fakeSource{data: load(t, "several.txt")}}
	obs, err := p.Collect(context.Background(), map[string]any{"arrays": []any{"md1", "md1", "md9"}})
	if err != nil {
		t.Fatal(err)
	}
	if find(obs, "md127", "raid.state") != nil || find(obs, "md0", "raid.state") != nil {
		t.Fatal("filtered arrays reported")
	}
	if o := find(obs, "md1", "raid.state"); o == nil || *o.Label != "degraded" {
		t.Fatalf("md1 = %+v", o)
	}
	if o := find(obs, "", "raid.arrays"); o == nil || *o.Value != 1 {
		t.Fatalf("arrays = %+v", o)
	}
	n := 0
	for _, o := range obs {
		if o.Item == "md1" && o.Metric == "raid.state" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("md1 state reported %d times", n)
	}
}

func TestCollectZeroArrays(t *testing.T) {
	for name, src := range map[string]fakeSource{
		"empty":   {data: load(t, "empty.txt")},
		"missing": {err: fmt.Errorf("open: %w", fs.ErrNotExist)},
	} {
		t.Run(name, func(t *testing.T) {
			obs, err := raidPlugin{Source: src}.Collect(context.Background(), nil)
			if err != nil || len(obs) != 1 {
				t.Fatalf("obs=%+v err=%v", obs, err)
			}
			if o := find(obs, "", "raid.arrays"); o == nil || *o.Value != 0 || o.Err != "" {
				t.Fatalf("arrays = %+v", o)
			}
		})
	}
}

func TestCollectReadError(t *testing.T) {
	obs, err := raidPlugin{Source: fakeSource{err: errors.New("boom")}}.Collect(context.Background(), nil)
	if err == nil || len(obs) != 1 {
		t.Fatalf("obs=%+v err=%v", obs, err)
	}
	o := find(obs, "", "raid.arrays")
	if o == nil || o.Err == "" || !strings.Contains(o.Err, "boom") {
		t.Fatalf("obs = %+v", obs)
	}
}

func TestCollectUnreportableItem(t *testing.T) {
	data := []byte("Personalities :\n" + strings.Repeat("x", 1000) + " : active raid1 a[0] b[1]\n      1 blocks [2/2] [UU]\n\nunused devices: <none>\n")
	obs, err := raidPlugin{Source: fakeSource{data: data}}.Collect(context.Background(), nil)
	if err == nil {
		t.Fatal("want advisory error")
	}
	if o := find(obs, "", "raid.arrays"); o == nil || *o.Value != 0 {
		t.Fatalf("arrays = %+v", o)
	}
}

func TestCollectBadParam(t *testing.T) {
	_, err := raidPlugin{Source: fakeSource{}}.Collect(context.Background(), map[string]any{"arrays": 5})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestName(t *testing.T) {
	if (raidPlugin{}).Name() != "raid" {
		t.Fatal("name")
	}
}

func TestCollectIMSMCountsOnlySubarray(t *testing.T) {
	obs, err := raidPlugin{Source: fakeSource{data: load(t, "imsm.txt")}}.Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if find(obs, "md127", "raid.state") != nil || find(obs, "md126", "raid.state") == nil {
		t.Fatalf("obs = %+v", obs)
	}
	if o := find(obs, "", "raid.arrays"); o == nil || *o.Value != 1 {
		t.Fatalf("arrays = %+v", o)
	}
}
