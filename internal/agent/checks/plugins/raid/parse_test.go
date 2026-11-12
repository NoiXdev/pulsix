package raid

import (
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseMdstat(t *testing.T) {
	tests := []struct {
		file string
		want []array
	}{
		{"clean_raid1.txt", []array{{name: "md0", state: "clean", total: 2, active: 2}}},
		{"degraded_raid1.txt", []array{{name: "md0", state: "degraded", total: 2, active: 1}}},
		{"recovering.txt", []array{{name: "md0", state: "recovering", total: 2, active: 1, syncPct: 12.3, hasPct: true}}},
		{"resync_pending.txt", []array{{name: "md1", state: "resyncing", total: 2, active: 2}}},
		{"resync_delayed.txt", []array{{name: "md1", state: "resyncing", total: 2, active: 2}}},
		{"resync_running.txt", []array{{name: "md1", state: "resyncing", total: 2, active: 2, syncPct: 25.5, hasPct: true}}},
		{"check.txt", []array{{name: "md0", state: "checking", total: 2, active: 2, syncPct: 30, hasPct: true}}},
		{"reshape.txt", []array{{name: "md0", state: "reshaping", total: 4, active: 4, syncPct: 11.1, hasPct: true}}},
		{"inactive.txt", []array{{name: "md0", state: "inactive", total: 2, active: 0}}},
		{"raid0.txt", []array{{name: "md0", state: "clean", total: 2, active: 2}}},
		{"raid0_failed.txt", []array{{name: "md0", state: "degraded", total: 2, active: 1, failed: 1}}},
		{"raid5_spare_failed.txt", []array{{name: "md2", state: "degraded", total: 4, active: 3, failed: 1}}},
		{"raid6.txt", []array{{name: "md3", state: "clean", total: 6, active: 6}}},
		{"raid10.txt", []array{{name: "md4", state: "clean", total: 4, active: 4}}},
		{"several.txt", []array{
			{name: "md127", state: "clean", total: 3, active: 3},
			{name: "md1", state: "degraded", total: 2, active: 1},
			{name: "md0", state: "resyncing", total: 2, active: 2},
		}},
		{"empty.txt", nil},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got := parseMdstat(load(t, tt.file))
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("array %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
