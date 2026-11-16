package runrequests_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
)

func TestFingerprintUnchangedWithoutFollowUps(t *testing.T) {
	id := uuid.MustParse("01930000-0000-7000-8000-000000000001")
	p := runrequests.Picture{
		TemplateID: "01930000-0000-7000-8000-000000000002", TemplateName: "cloudpanel", TemplateRevision: 3, Limit: "web-*",
		Playbook:        runrequests.PicturePlaybook{ID: "01930000-0000-7000-8000-000000000003", Source: "repo", Path: "cloudpanel.yml"},
		Targets:         []runrequests.PictureHost{{ID: "01930000-0000-7000-8000-000000000004", Name: "k10001-cp-01", Address: "10.0.0.1"}},
		VaultIdentities: []runrequests.PictureVault{}, ExtraVars: map[string]any{"a": "b"}, VarsDigest: "d",
	}
	if got, want := runrequests.Fingerprint(id, p), "343fc0714d93b72e718bad778a2a2b696344b63db23f729f61cef298d2df496b"; got != want {
		t.Errorf("run fingerprint = %s, want %s", got, want)
	}
	p.Retry = &runrequests.PictureRetry{JobID: "01930000-0000-7000-8000-000000000005", FailedHosts: []string{"01930000-0000-7000-8000-000000000004"}}
	if got, want := runrequests.Fingerprint(id, p), "308c74fdd6b900669e61fb8e20b834f34ed3b6fd47813a3110d72105497e25e9"; got != want {
		t.Errorf("retry fingerprint = %s, want %s", got, want)
	}
}

// fuTree is a map-backed template graph; calls counts lookups.
type fuTree struct {
	byID  map[uuid.UUID]runrequests.FollowUpTemplate
	calls int
	err   error
}

func (f *fuTree) lookup(_ context.Context, id uuid.UUID) (runrequests.FollowUpTemplate, bool, error) {
	f.calls++
	if f.err != nil {
		return runrequests.FollowUpTemplate{}, false, f.err
	}
	t, ok := f.byID[id]
	return t, ok, nil
}

func fuID(n int) uuid.UUID { return uuid.MustParse(fmt.Sprintf("01930000-0000-7000-8000-%012d", n)) }

func fuNode(n int, rev int64, stop string, children ...runrequests.PictureFollowUp) runrequests.PictureFollowUp {
	name := fmt.Sprintf("t%d", n)
	return runrequests.PictureFollowUp{TemplateID: fuID(n).String(), TemplateName: name, TemplateRevision: rev, Stop: stop, FollowUps: children}
}

func fuRoot(n int, depth int, path ...int) runrequests.FollowUpRoot {
	var p []uuid.UUID
	for _, x := range path {
		p = append(p, fuID(x))
	}
	return runrequests.FollowUpRoot{TemplateID: fuID(n), Name: fmt.Sprintf("t%d", n), Depth: depth, Path: p}
}

func fuT(n int, rev int64, children ...int) (uuid.UUID, runrequests.FollowUpTemplate) {
	t := runrequests.FollowUpTemplate{Name: fmt.Sprintf("t%d", n), Revision: rev}
	for _, c := range children {
		t.FollowUps = append(t.FollowUps, runrequests.FollowUpRef{TemplateID: fuID(c), Name: fmt.Sprintf("t%d", c)})
	}
	return fuID(n), t
}

func fuGraph(entries ...func() (uuid.UUID, runrequests.FollowUpTemplate)) *fuTree {
	g := &fuTree{byID: map[uuid.UUID]runrequests.FollowUpTemplate{}}
	for _, e := range entries {
		id, t := e()
		g.byID[id] = t
	}
	return g
}

func fuE(n int, rev int64, children ...int) func() (uuid.UUID, runrequests.FollowUpTemplate) {
	return func() (uuid.UUID, runrequests.FollowUpTemplate) { return fuT(n, rev, children...) }
}

func countNodes(ns []runrequests.PictureFollowUp) int {
	c := 0
	for _, n := range ns {
		c += 1 + countNodes(n.FollowUps)
	}
	return c
}

func TestBuildFollowUps(t *testing.T) {
	const x, y, z, w = 1, 2, 3, 4
	ctx := context.Background()

	t.Run("no roots", func(t *testing.T) {
		g := fuGraph()
		got, err := runrequests.BuildFollowUps(ctx, true, nil, g.lookup)
		if err != nil || got != nil {
			t.Fatalf("got %+v, %v; want nil", got, err)
		}
	})

	t.Run("start false", func(t *testing.T) {
		g := fuGraph(fuE(y, 4, z))
		got, err := runrequests.BuildFollowUps(ctx, false, []runrequests.FollowUpRoot{fuRoot(y, 1, x)}, g.lookup)
		if err != nil {
			t.Fatal(err)
		}
		want := &runrequests.PictureFollowUps{Start: false, Templates: []runrequests.PictureFollowUp{{TemplateID: fuID(y).String(), TemplateName: "t2"}}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
		if g.calls != 0 {
			t.Errorf("lookup called %d times", g.calls)
		}
	})

	tests := []struct {
		name  string
		g     *fuTree
		roots []runrequests.FollowUpRoot
		want  []runrequests.PictureFollowUp
	}{
		{
			"nested", fuGraph(fuE(y, 4, z), fuE(z, 2)),
			[]runrequests.FollowUpRoot{fuRoot(y, 1, x)},
			[]runrequests.PictureFollowUp{fuNode(y, 4, "", fuNode(z, 2, ""))},
		},
		{
			"cycle", fuGraph(fuE(y, 4, x)),
			[]runrequests.FollowUpRoot{fuRoot(y, 1, x)},
			[]runrequests.PictureFollowUp{fuNode(y, 4, "", runrequests.PictureFollowUp{TemplateID: fuID(x).String(), TemplateName: "t1", Stop: runrequests.StopInChain})},
		},
		{
			"root on its own path", fuGraph(fuE(y, 4)),
			[]runrequests.FollowUpRoot{fuRoot(y, 2, x, y)},
			[]runrequests.PictureFollowUp{{TemplateID: fuID(y).String(), TemplateName: "t2", Stop: runrequests.StopInChain}},
		},
		{
			"root too deep", fuGraph(fuE(y, 4)),
			[]runrequests.FollowUpRoot{fuRoot(y, 10, x)},
			[]runrequests.PictureFollowUp{{TemplateID: fuID(y).String(), TemplateName: "t2", Stop: runrequests.StopTooDeep}},
		},
		{
			"deleted", fuGraph(),
			[]runrequests.FollowUpRoot{fuRoot(y, 1, x)},
			[]runrequests.PictureFollowUp{{TemplateID: fuID(y).String(), TemplateName: "t2", Stop: runrequests.StopDeleted}},
		},
		{
			"diamond", fuGraph(fuE(y, 1, w), fuE(z, 1, w), fuE(w, 7)),
			[]runrequests.FollowUpRoot{fuRoot(y, 1, x), fuRoot(z, 1, x)},
			[]runrequests.PictureFollowUp{fuNode(y, 1, "", fuNode(w, 7, "")), fuNode(z, 1, "", fuNode(w, 7, ""))},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runrequests.BuildFollowUps(ctx, true, tc.roots, tc.g.lookup)
			if err != nil {
				t.Fatal(err)
			}
			want := &runrequests.PictureFollowUps{Start: true, Templates: tc.want}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}

	t.Run("depth limit", func(t *testing.T) {
		// X(1) -> F1 -> ... -> F10: F1..F9 are walked, F10 is too_deep.
		g := &fuTree{byID: map[uuid.UUID]runrequests.FollowUpTemplate{}}
		for i := 1; i <= 10; i++ {
			id, tpl := fuT(100+i, int64(i), 100+i+1)
			g.byID[id] = tpl
		}
		got, err := runrequests.BuildFollowUps(ctx, true, []runrequests.FollowUpRoot{fuRoot(101, 1, x)}, g.lookup)
		if err != nil {
			t.Fatal(err)
		}
		n := got.Templates[0]
		for i := 1; i <= 9; i++ {
			if n.Stop != "" || n.TemplateRevision != int64(i) || len(n.FollowUps) != 1 {
				t.Fatalf("F%d = %+v", i, n)
			}
			n = n.FollowUps[0]
		}
		if n.TemplateID != fuID(110).String() || n.TemplateName != "t110" || n.Stop != runrequests.StopTooDeep || n.TemplateRevision != 0 || len(n.FollowUps) != 0 {
			t.Errorf("F10 = %+v", n)
		}
		if g.calls != 9 {
			t.Errorf("lookup called %d times, want 9", g.calls)
		}
	})

	t.Run("cap", func(t *testing.T) {
		g := &fuTree{byID: map[uuid.UUID]runrequests.FollowUpTemplate{}}
		var kids []int
		for i := 0; i < 10; i++ {
			kids = append(kids, 10+i)
		}
		id, tpl := fuT(2, 1, kids...)
		g.byID[id] = tpl
		for _, k := range kids {
			var grand []int
			for j := 0; j < 10; j++ {
				grand = append(grand, 1000+(k-10)*10+j)
			}
			id, tpl := fuT(k, 1, grand...)
			g.byID[id] = tpl
			for _, gr := range grand {
				id, tpl := fuT(gr, 1)
				g.byID[id] = tpl
			}
		}
		got, err := runrequests.BuildFollowUps(ctx, true, []runrequests.FollowUpRoot{fuRoot(2, 1, x)}, g.lookup)
		if err != nil {
			t.Fatal(err)
		}
		if c := countNodes(got.Templates); c != runrequests.MaxPictureFollowUps {
			t.Errorf("nodes = %d, want %d", c, runrequests.MaxPictureFollowUps)
		}
		if !got.Truncated {
			t.Error("Truncated not set")
		}
		if s := got.Startable(); len(s) != 0 {
			t.Errorf("Startable = %v, want none", s)
		}
	})

	t.Run("lookup error", func(t *testing.T) {
		boom := errors.New("boom")
		g := &fuTree{err: boom}
		_, err := runrequests.BuildFollowUps(ctx, true, []runrequests.FollowUpRoot{fuRoot(y, 1, x)}, g.lookup)
		if !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestStartable(t *testing.T) {
	f := &runrequests.PictureFollowUps{Start: true, Templates: []runrequests.PictureFollowUp{
		fuNode(1, 1, ""), fuNode(2, 0, runrequests.StopDeleted), fuNode(3, 1, runrequests.StopInChain), fuNode(4, 1, ""),
	}}
	if got, want := f.Startable(), []uuid.UUID{fuID(1), fuID(4)}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	f.Start = false
	if got := f.Startable(); len(got) != 0 {
		t.Errorf("start=false: got %v", got)
	}
	if got := (*runrequests.PictureFollowUps)(nil).Startable(); len(got) != 0 {
		t.Errorf("nil: got %v", got)
	}
}
