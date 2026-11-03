package versioncompat

import (
	"sync"
	"testing"
)

func TestOf(t *testing.T) {
	tests := []struct {
		name               string
		manager, component string
		want               Status
	}{
		{"same version", "v1.4.0", "v1.4.0", Current},
		{"patch is ignored, older", "v1.2.3", "1.2.0", Current},
		{"patch is ignored, newer", "v1.2.3", "v1.2.9", Current},
		{"with and without the v", "v1.2.3", "1.2.9", Current},
		{"manager without the v", "1.2.3", "v1.2.1", Current},
		{"previous minor", "v1.4.0", "v1.3.7", PreviousMinor},
		{"two minors below", "v1.4.0", "v1.2.0", UpdateRequired},
		{"many minors below", "v1.9.0", "v1.0.0", UpdateRequired},
		{"newer minor", "v1.4.0", "v1.5.0", Newer},
		{"much newer minor", "v1.4.0", "v1.9.2", Newer},
		{"older major", "v2.0.0", "v1.9.0", UpdateRequired},
		{"newer major", "v1.9.0", "v2.0.0", Newer},
		{"pre-release suffix is ignored", "v1.4.0", "v1.4.0-rc.1", Current},
		{"build metadata is ignored", "v1.4.0", "v1.3.0+abc", PreviousMinor},
		{"git describe style", "v1.4.0", "v1.4.0-12-gabc1234", Current},
		{"0.x previous minor", "v0.4.0", "v0.3.2", PreviousMinor},
		{"0.x two below", "v0.4.0", "v0.2.0", UpdateRequired},

		{"manager is dev", "dev", "v1.4.0", Unknown},
		{"component is dev", "v1.4.0", "dev", Unknown},
		{"both dev", "dev", "dev", Unknown},
		{"component empty", "v1.4.0", "", Unknown},
		{"manager empty", "", "v1.4.0", Unknown},
		{"both empty", "", "", Unknown},
		{"component embedded", "v1.4.0", "embedded", Unknown},
		{"component two parts", "v1.4.0", "v1.4", Unknown},
		{"component four parts", "v1.4.0", "v1.4.0.1", Unknown},
		{"component non-numeric", "v1.4.0", "v1.x.0", Unknown},
		{"component negative", "v1.4.0", "v1.-4.0", Unknown},
		{"manager unparseable", "snapshot", "v1.4.0", Unknown},
		{"whitespace only", "v1.4.0", "  ", Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Of(tt.manager, tt.component); got != tt.want {
				t.Errorf("Of(%q, %q) = %q, want %q", tt.manager, tt.component, got, tt.want)
			}
		})
	}
}

func TestNeedsAttention(t *testing.T) {
	for s, want := range map[Status]bool{
		Current: false, PreviousMinor: false, Unknown: false, UpdateRequired: true, Newer: true,
	} {
		if got := s.NeedsAttention(); got != want {
			t.Errorf("%q.NeedsAttention() = %v, want %v", s, got, want)
		}
	}
}

func TestWarnerWarnsOncePerComponent(t *testing.T) {
	var w Warner
	if st, first := w.Observe("worker/a", "v1.4.0", "v1.1.0"); st != UpdateRequired || !first {
		t.Fatalf("first sighting = (%q, %v), want (update_required, true)", st, first)
	}
	if st, first := w.Observe("worker/a", "v1.4.0", "v1.1.0"); st != UpdateRequired || first {
		t.Fatalf("second sighting = (%q, %v), want (update_required, false)", st, first)
	}
	if _, first := w.Observe("worker/b", "v1.4.0", "v1.9.0"); !first {
		t.Fatal("another component must warn on its own first sighting")
	}
	if _, first := w.Observe("agent/a", "v1.4.0", "v1.1.0"); !first {
		t.Fatal("the same id under another kind is another component")
	}
}

func TestWarnerNeverWarnsForAcceptableVersions(t *testing.T) {
	var w Warner
	for _, v := range []string{"v1.4.1", "v1.3.0", "dev", "", "embedded"} {
		if st, first := w.Observe("worker/a", "v1.4.0", v); first {
			t.Errorf("version %q (%q) must not warn", v, st)
		}
	}
	// A component that was fine and later goes stale still warns once.
	if _, first := w.Observe("worker/a", "v1.4.0", "v1.0.0"); !first {
		t.Error("a later stale version must warn")
	}
}

func TestWarnerConcurrent(t *testing.T) {
	var w Warner
	var wg sync.WaitGroup
	var mu sync.Mutex
	firsts := 0
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, first := w.Observe("worker/a", "v1.4.0", "v1.0.0"); first {
				mu.Lock()
				firsts++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firsts != 1 {
		t.Fatalf("%d goroutines saw a first sighting, want exactly 1", firsts)
	}
}
