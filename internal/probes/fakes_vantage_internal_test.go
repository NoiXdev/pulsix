package probes

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

// fakeAgents is the online, probe-capable agents per organization.
type fakeAgents struct {
	mu     sync.Mutex
	online map[uuid.UUID][]uuid.UUID
	err    error
	calls  int
}

func (f *fakeAgents) set(org uuid.UUID, agents ...uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.online == nil {
		f.online = map[uuid.UUID][]uuid.UUID{}
	}
	f.online[org] = agents
}

func (f *fakeAgents) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeAgents) OnlineProbeAgents(_ context.Context, org uuid.UUID) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return slices.Clone(f.online[org]), nil
}

func (f *fakeAgents) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeGroups is the monitoring groups: group id → member hosts. A group
// that is not set does not exist. Its selector is "group:<id>".
type fakeGroups struct {
	mu       sync.Mutex
	members  map[uuid.UUID][]uuid.UUID
	gets     map[uuid.UUID]int
	previews int
}

func (f *fakeGroups) set(group uuid.UUID, hosts ...uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.members == nil {
		f.members = map[uuid.UUID][]uuid.UUID{}
	}
	f.members[group] = hosts
}

func (f *fakeGroups) GetGroup(_ context.Context, org, id uuid.UUID) (checks.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gets == nil {
		f.gets = map[uuid.UUID]int{}
	}
	f.gets[id]++
	if _, ok := f.members[id]; !ok {
		return checks.Group{}, checks.ErrNotFound
	}
	return checks.Group{ID: id, OrgID: org, Selector: "group:" + id.String()}, nil
}

func (f *fakeGroups) Preview(_ context.Context, _ uuid.UUID, sel string) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.previews++
	id := uuid.MustParse(strings.TrimPrefix(sel, "group:"))
	return slices.Clone(f.members[id]), nil
}

func (f *fakeGroups) Gets() map[uuid.UUID]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uuid.UUID]int{}
	for k, v := range f.gets {
		out[k] = v
	}
	return out
}

type replaceCall struct {
	org uuid.UUID
	set []Assignment
}

type vantageRecord struct {
	org     uuid.UUID
	vantage string
	obs     []checks.Observation
}

type deleteCall struct {
	org, host uuid.UUID
	key       string
	keep      []string
}

type vantageKey struct {
	host                       uuid.UUID
	key, vantage, item, metric string
}

// fakeVantageStore is the per-vantage store in memory.
type fakeVantageStore struct {
	mu       sync.Mutex
	replaces []replaceCall
	records  []vantageRecord
	deletes  []deleteCall
	reads    int
	current  map[vantageKey]VantageRow

	replaceErr, readErr, deleteErr error
}

func (f *fakeVantageStore) ReplaceOrgAssignments(_ context.Context, org uuid.UUID, want []Assignment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replaceErr != nil {
		return f.replaceErr
	}
	f.replaces = append(f.replaces, replaceCall{org: org, set: slices.Clone(want)})
	return nil
}

func (f *fakeVantageStore) RecordVantage(_ context.Context, org uuid.UUID, vantage string, obs []checks.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, vantageRecord{org: org, vantage: vantage, obs: slices.Clone(obs)})
	for _, o := range obs {
		f.putLocked(o.HostID, o.CheckKey, VantageRow{
			Vantage: vantage, Item: o.Item, Metric: o.Metric, At: o.At, Value: o.Value, Label: o.Label, Err: o.Err,
		})
	}
	return nil
}

func (f *fakeVantageStore) put(host uuid.UUID, key string, rows ...VantageRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range rows {
		f.putLocked(host, key, r)
	}
}

func (f *fakeVantageStore) putLocked(host uuid.UUID, key string, r VantageRow) {
	if f.current == nil {
		f.current = map[vantageKey]VantageRow{}
	}
	f.current[vantageKey{host: host, key: key, vantage: r.Vantage, item: r.Item, metric: r.Metric}] = r
}

func (f *fakeVantageStore) VantageCurrent(_ context.Context, _, host uuid.UUID, key string) ([]VantageRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.readErr != nil {
		return nil, f.readErr
	}
	var out []VantageRow
	for k, r := range f.current {
		if k.host == host && k.key == key {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b VantageRow) int {
		return cmp.Or(cmp.Compare(a.Vantage, b.Vantage), cmp.Compare(a.Item, b.Item), cmp.Compare(a.Metric, b.Metric))
	})
	return out, nil
}

func (f *fakeVantageStore) DeleteVantages(_ context.Context, org, host uuid.UUID, key string, keep []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletes = append(f.deletes, deleteCall{org: org, host: host, key: key, keep: slices.Clone(keep)})
	for k := range f.current {
		if k.host == host && k.key == key && !slices.Contains(keep, k.vantage) {
			delete(f.current, k)
		}
	}
	return nil
}

func (f *fakeVantageStore) setErrs(replace, read, del error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaceErr, f.readErr, f.deleteErr = replace, read, del
}

func (f *fakeVantageStore) Replaces() []replaceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.replaces)
}

func (f *fakeVantageStore) Records() []vantageRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.records)
}

func (f *fakeVantageStore) Deletes() []deleteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deletes)
}

func (f *fakeVantageStore) Reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}
