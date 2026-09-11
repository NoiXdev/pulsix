package hosts_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/hosts/selector"
)

// The tag-related fakeHostStore methods below extend the same type
// service_test.go defines and every other Service test in this package
// already drives — a second, separate fake would duplicate CreateHost/
// GetHost/etc. for no benefit, and Go methods for one type may be split
// across any file in its package.

func (f *fakeHostStore) ListHostTags(_ context.Context, orgID, hostID uuid.UUID) ([]hosts.Tag, error) {
	if err := f.take("ListHostTags"); err != nil {
		return nil, err
	}
	h, ok := f.list[hostID]
	if !ok || h.OrgID != orgID {
		return nil, hosts.ErrNotFound
	}
	return append([]hosts.Tag(nil), f.tags[hostID]...), nil
}

// ReplaceManualTags mirrors the real schema's "manual rows keyed by
// (host_id, key, source)" semantics: every existing manual tag is dropped
// and replaced wholesale, while every fact tag survives untouched — the
// same delete-then-insert-manual-only shape SQLStore.ReplaceManualTags
// gives it against the real host_tags table.
func (f *fakeHostStore) ReplaceManualTags(_ context.Context, orgID, hostID uuid.UUID, tags []hosts.Tag) error {
	if err := f.take("ReplaceManualTags"); err != nil {
		return err
	}
	h, ok := f.list[hostID]
	if !ok || h.OrgID != orgID {
		return hosts.ErrNotFound
	}
	var kept []hosts.Tag
	for _, t := range f.tags[hostID] {
		if t.Source != hosts.TagSourceManual {
			kept = append(kept, t)
		}
	}
	kept = append(kept, tags...)
	f.tags[hostID] = kept
	return nil
}

func (f *fakeHostStore) ListTagKeyValueCounts(_ context.Context, orgID uuid.UUID) ([]hosts.TagValueCount, error) {
	if err := f.take("ListTagKeyValueCounts"); err != nil {
		return nil, err
	}
	type kv struct{ key, value string }
	counts := map[kv]int{}
	for hostID, tags := range f.tags {
		h, ok := f.list[hostID]
		if !ok || h.OrgID != orgID {
			continue
		}
		seen := map[kv]bool{}
		for _, t := range tags {
			pair := kv{t.Key, t.Value}
			if !seen[pair] {
				counts[pair]++
				seen[pair] = true
			}
		}
	}
	out := make([]hosts.TagValueCount, 0, len(counts))
	for pair, n := range counts {
		out = append(out, hosts.TagValueCount{Key: pair.key, Value: pair.value, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Value < out[j].Value
	})
	return out, nil
}

// mergedTags computes the effective tag map for one host's raw (manual and
// fact) rows: a fact value applies first, a manual value on the same key
// always overrides it — the precedence Service.SetTags' own doc comment
// describes, and the one selector.Selector.Matches is handed.
func mergedTags(tags []hosts.Tag) map[string]string {
	merged := map[string]string{}
	for _, t := range tags {
		if t.Source == hosts.TagSourceFact {
			merged[t.Key] = t.Value
		}
	}
	for _, t := range tags {
		if t.Source == hosts.TagSourceManual {
			merged[t.Key] = t.Value
		}
	}
	return merged
}

func (f *fakeHostStore) QueryHosts(_ context.Context, orgID uuid.UUID, sel selector.Selector) ([]hosts.Host, error) {
	if err := f.take("QueryHosts"); err != nil {
		return nil, err
	}
	var out []hosts.Host
	for _, h := range f.list {
		if h.OrgID != orgID {
			continue
		}
		if sel.Matches(mergedTags(f.tags[h.ID])) {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// seedFact writes a fact tag directly into the fake store's tag map,
// bypassing Service — which, by design, has no route to write a fact tag
// itself (see Service.SetTags' own doc comment); a later task's
// agent-facts pipeline is what will do this for real.
func (f *fakeHostStore) seedFact(hostID uuid.UUID, key, value string) {
	f.tags[hostID] = append(f.tags[hostID], hosts.Tag{Key: key, Value: value, Source: hosts.TagSourceFact})
}

func TestSetTagsRejectsBadKey(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	h, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID})
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range []hosts.Tag{
		{Key: "Role", Value: "db"},                 // uppercase
		{Key: "has space", Value: "x"},             // space
		{Key: "", Value: "x"},                      // empty
		{Key: strings.Repeat("k", 65), Value: "x"}, // too long (65 > 64)
	} {
		if err := svc.SetTags(ctx, org, h.ID, []hosts.Tag{bad}); !errors.Is(err, hosts.ErrInvalidInput) {
			t.Errorf("tag %+v: err = %v, want ErrInvalidInput", bad, err)
		}
	}
}

func TestSetTagsRejectsOverLongValue(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	h, _ := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID})

	tag := hosts.Tag{Key: "note", Value: strings.Repeat("x", 257)}
	if err := svc.SetTags(ctx, org, h.ID, []hosts.Tag{tag}); !errors.Is(err, hosts.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestSetTagsRejectsMoreThan50Tags(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	h, _ := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID})

	tags := make([]hosts.Tag, 51)
	for i := range tags {
		tags[i] = hosts.Tag{Key: "k" + string(rune('a'+i%26)) + string(rune('0'+i/26)), Value: "v"}
	}
	if err := svc.SetTags(ctx, org, h.ID, tags); !errors.Is(err, hosts.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestSetTagsRejectsDuplicateKey(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	h, _ := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID})

	tags := []hosts.Tag{{Key: "role", Value: "db"}, {Key: "role", Value: "web"}}
	if err := svc.SetTags(ctx, org, h.ID, tags); !errors.Is(err, hosts.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestSetTagsUnknownHost(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	if err := svc.SetTags(ctx, org, uuid.Must(uuid.NewV7()), []hosts.Tag{{Key: "role", Value: "db"}}); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestSetTagsReplacesManualKeepsFact is the brief's central tag-rule test:
// SetTags replaces the manual set wholesale but never touches a fact tag,
// and a manual tag sharing a fact tag's key wins in the merged view.
func TestSetTagsReplacesManualKeepsFact(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	h, _ := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID})

	// Seed fact tags directly on the fake store, the way an agent-facts
	// pipeline (a later task) would — Service has no route to write a fact
	// tag itself yet.
	st.seedFact(h.ID, "os", "debian")
	st.seedFact(h.ID, "role", "web") // will be overridden by a manual "role" below

	if err := svc.SetTags(ctx, org, h.ID, []hosts.Tag{
		{Key: "role", Value: "db"},
		{Key: "env", Value: "prod"},
	}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}

	got, err := svc.ListTags(ctx, org, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	byKeySource := map[string]hosts.Tag{}
	for _, tag := range got {
		byKeySource[tag.Key+"/"+string(tag.Source)] = tag
	}
	if tag, ok := byKeySource["os/fact"]; !ok || tag.Value != "debian" {
		t.Fatalf("fact tag 'os' should survive untouched: %+v", got)
	}
	if tag, ok := byKeySource["role/manual"]; !ok || tag.Value != "db" {
		t.Fatalf("manual tag 'role' should be db: %+v", got)
	}
	if tag, ok := byKeySource["role/fact"]; !ok || tag.Value != "web" {
		t.Fatalf("fact tag 'role' should still exist (untouched) at its original value: %+v", got)
	}
	if tag, ok := byKeySource["env/manual"]; !ok || tag.Value != "prod" {
		t.Fatalf("manual tag 'env' should be prod: %+v", got)
	}

	// A second SetTags call that omits "env" must remove only the manual
	// "env" tag (a full replace, not a merge) while still leaving every
	// fact tag alone.
	if err := svc.SetTags(ctx, org, h.ID, []hosts.Tag{{Key: "role", Value: "db"}}); err != nil {
		t.Fatalf("SetTags (second call): %v", err)
	}
	got, err = svc.ListTags(ctx, org, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range got {
		if tag.Key == "env" {
			t.Fatalf("manual tag 'env' should have been replaced away: %+v", got)
		}
	}
}

func TestListTagsUnknownHost(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	if _, err := svc.ListTags(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestListTagsInvisibleAcrossOrgs proves a host id from another org is
// exactly as unusable for tags as it is for the host itself
// (TestGetHostInvisibleAcrossOrgs, service_test.go).
func TestListTagsInvisibleAcrossOrgs(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	zoneA, _ := svc.CreateZone(ctx, orgA, "fra", "F")
	h, _ := svc.CreateHost(ctx, orgA, hosts.HostInput{Name: "web-01", ZoneID: zoneA.ID})

	if _, err := svc.ListTags(ctx, orgB, h.ID); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := svc.SetTags(ctx, orgB, h.ID, []hosts.Tag{{Key: "role", Value: "db"}}); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestQueryHostsMergesFactAndManual proves QueryHosts(ctx, orgID, sel)
// matches against the *merged* view (manual overriding fact), the same
// property TestSetTagsReplacesManualKeepsFact exercises for ListTags.
func TestQueryHostsMergesFactAndManual(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")

	dbHost, _ := svc.CreateHost(ctx, org, hosts.HostInput{Name: "db-01", ZoneID: zone.ID})
	webHost, _ := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID})

	// A host in a different organization, also tagged role=db, must never
	// appear in org's own QueryHosts results — the same org-scoping
	// ListHosts already guarantees.
	otherOrg := uuid.Must(uuid.NewV7())
	otherZone, _ := svc.CreateZone(ctx, otherOrg, "fra", "F")
	otherOrgHost, _ := svc.CreateHost(ctx, otherOrg, hosts.HostInput{Name: "other-01", ZoneID: otherZone.ID})
	st.seedFact(otherOrgHost.ID, "role", "db")

	// dbHost: fact says role=web, but a manual tag overrides it to role=db
	// — QueryHosts must see the manual value, not the fact one underneath.
	st.seedFact(dbHost.ID, "role", "web")
	if err := svc.SetTags(ctx, org, dbHost.ID, []hosts.Tag{{Key: "role", Value: "db"}}); err != nil {
		t.Fatal(err)
	}
	// webHost: no manual override, fact value applies as-is.
	st.seedFact(webHost.ID, "role", "web")

	sel, err := selector.Parse(`role = db`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.QueryHosts(ctx, org, sel)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != dbHost.ID {
		t.Fatalf("QueryHosts(role = db) = %+v, want just dbHost", got)
	}

	sel, err = selector.Parse(`role = web`)
	if err != nil {
		t.Fatal(err)
	}
	got, err = svc.QueryHosts(ctx, org, sel)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != webHost.ID {
		t.Fatalf("QueryHosts(role = web) = %+v, want just webHost (the overridden dbHost must not match its old fact value)", got)
	}
}

func TestListTagKeys(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	h1, _ := svc.CreateHost(ctx, org, hosts.HostInput{Name: "a-host", ZoneID: zone.ID})
	h2, _ := svc.CreateHost(ctx, org, hosts.HostInput{Name: "b-host", ZoneID: zone.ID})

	if err := svc.SetTags(ctx, org, h1.ID, []hosts.Tag{{Key: "role", Value: "db"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetTags(ctx, org, h2.ID, []hosts.Tag{{Key: "role", Value: "web"}}); err != nil {
		t.Fatal(err)
	}

	keys, err := svc.ListTagKeys(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Key != "role" {
		t.Fatalf("keys = %+v, want one key 'role'", keys)
	}
	if len(keys[0].Values) != 2 {
		t.Fatalf("values = %+v, want db and web", keys[0].Values)
	}
	for _, v := range keys[0].Values {
		if v.Count != 1 {
			t.Fatalf("count for %q = %d, want 1", v.Value, v.Count)
		}
	}
}
