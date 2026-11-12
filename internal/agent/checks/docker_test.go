package checks

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDockerEngine struct {
	mu         sync.Mutex
	containers []containerSummary
	listErr    error
	details    map[string]containerDetail
	inspectErr map[string]error
	gotFilter  string
	listed     bool

	// inspectDelay, when set, holds each Inspect so concurrency is observable.
	inspectDelay time.Duration
	inflight     atomic.Int32
	peak         atomic.Int32
	// blockUntilDone makes List wait for ctx.
	blockUntilDone bool
}

func (f *fakeDockerEngine) List(ctx context.Context, labelFilter string) ([]containerSummary, error) {
	f.mu.Lock()
	f.gotFilter = labelFilter
	f.listed = true
	f.mu.Unlock()
	if f.blockUntilDone {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.containers, nil
}

func (f *fakeDockerEngine) Inspect(ctx context.Context, id string) (containerDetail, error) {
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if f.inspectDelay > 0 {
		time.Sleep(f.inspectDelay)
	}
	if err := f.inspectErr[id]; err != nil {
		return containerDetail{}, err
	}
	return f.details[id], nil
}

func newTestDocker(f *fakeDockerEngine, gotSocket *string) dockerPlugin {
	return dockerPlugin{engine: func(socket string) (dockerEngine, error) {
		if gotSocket != nil {
			*gotSocket = socket
		}
		return f, nil
	}}
}

func basicDockerFake() *fakeDockerEngine {
	return &fakeDockerEngine{
		containers: []containerSummary{
			{ID: "a", Name: "web", State: "running"},
			{ID: "b", Name: "job", State: "exited"},
			{ID: "c", Name: "cron", State: "running", Labels: map[string]string{"pulsix.monitor": "false"}},
		},
		details: map[string]containerDetail{
			"a": {Health: "healthy"},
			"b": {Health: "none", ExitCode: 3, RestartCount: 1},
			"c": {Health: "none"},
		},
	}
}

func hasObs(obs []Observation, item, metric string) bool {
	for _, o := range obs {
		if o.Item == item && o.Metric == metric {
			return true
		}
	}
	return false
}

func TestDockerAllSelection(t *testing.T) {
	f := basicDockerFake()
	obs, err := newTestDocker(f, nil).Collect(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if f.gotFilter != "" {
		t.Errorf("filter = %q, want empty", f.gotFilter)
	}
	if hasObs(obs, "cron", "docker.state") {
		t.Error("opted-out container reported")
	}
	if l := findObs(t, obs, "web", "docker.state").Label; l == nil || *l != "running" {
		t.Errorf("web state = %v", l)
	}
	if l := findObs(t, obs, "web", "docker.health").Label; l == nil || *l != "healthy" {
		t.Errorf("web health = %v", l)
	}
	if v := findObs(t, obs, "web", "docker.restart_count").Value; v == nil || *v != 0 {
		t.Errorf("web restarts = %v", v)
	}
	if hasObs(obs, "web", "docker.exit_code") {
		t.Error("running container has exit_code")
	}
	if l := findObs(t, obs, "job", "docker.state").Label; l == nil || *l != "exited" {
		t.Errorf("job state = %v", l)
	}
	if v := findObs(t, obs, "job", "docker.exit_code").Value; v == nil || *v != 3 {
		t.Errorf("job exit = %v", v)
	}
	if v := findObs(t, obs, "", "docker.containers_running").Value; v == nil || *v != 1 {
		t.Errorf("running = %v", v)
	}
	if v := findObs(t, obs, "", "docker.containers_total").Value; v == nil || *v != 2 {
		t.Errorf("total = %v", v)
	}
}

func TestDockerOptOutValue(t *testing.T) {
	for _, tc := range []struct {
		value string
		kept  bool
	}{
		{"false", false}, {"FALSE", false}, {" false ", false},
		{"0", true}, {"no", true}, {"", true},
	} {
		f := &fakeDockerEngine{
			containers: []containerSummary{{ID: "x", Name: "x", State: "running", Labels: map[string]string{"pulsix.monitor": tc.value}}},
			details:    map[string]containerDetail{"x": {Health: "none"}},
		}
		obs, err := newTestDocker(f, nil).Collect(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := hasObs(obs, "x", "docker.state"); got != tc.kept {
			t.Errorf("value %q: reported = %v, want %v", tc.value, got, tc.kept)
		}
	}
}

func TestDockerLabeledSelection(t *testing.T) {
	for _, tc := range []struct {
		params map[string]any
		filter string
	}{
		{map[string]any{"selection": "labeled"}, "pulsix.monitor=true"},
		{map[string]any{"selection": "labeled", "label": "watch"}, "watch"},
	} {
		f := basicDockerFake()
		obs, err := newTestDocker(f, nil).Collect(context.Background(), tc.params)
		if err != nil {
			t.Fatal(err)
		}
		if f.gotFilter != tc.filter {
			t.Errorf("filter = %q, want %q", f.gotFilter, tc.filter)
		}
		// The engine did the filtering; the opted-out label is not applied here.
		if !hasObs(obs, "cron", "docker.state") {
			t.Error("labeled mode applied the opt-out client-side")
		}
	}
}

func TestDockerAllWithCustomLabelKey(t *testing.T) {
	f := &fakeDockerEngine{
		containers: []containerSummary{
			{ID: "x", Name: "x", State: "running", Labels: map[string]string{"com.example/watch": "false"}},
			{ID: "y", Name: "y", State: "running", Labels: map[string]string{"pulsix.monitor": "false"}},
		},
		details: map[string]containerDetail{"x": {Health: "none"}, "y": {Health: "none"}},
	}
	obs, err := newTestDocker(f, nil).Collect(context.Background(), map[string]any{"label": "com.example/watch=yes"})
	if err != nil {
		t.Fatal(err)
	}
	if hasObs(obs, "x", "docker.state") || !hasObs(obs, "y", "docker.state") {
		t.Errorf("custom opt-out key not honoured: %+v", obs)
	}
}

func TestDockerSocketParam(t *testing.T) {
	var got string
	f := basicDockerFake()
	if _, err := newTestDocker(f, &got).Collect(context.Background(), map[string]any{"socket": "/x.sock"}); err != nil || got != "/x.sock" {
		t.Errorf("socket = %q, err %v", got, err)
	}
	if _, err := newTestDocker(f, &got).Collect(context.Background(), map[string]any{}); err != nil || got != "" {
		t.Errorf("socket = %q, err %v", got, err)
	}
}

func TestDockerDefensiveParams(t *testing.T) {
	f := basicDockerFake()
	_, err := newTestDocker(f, nil).Collect(context.Background(), map[string]any{"selection": 5, "label": 7.0, "socket": []any{}})
	if err != nil || f.gotFilter != "" {
		t.Errorf("err %v filter %q", err, f.gotFilter)
	}
	_, err = newTestDocker(f, nil).Collect(context.Background(), map[string]any{"selection": "bogus"})
	if err != nil || f.gotFilter != "" {
		t.Errorf("unknown selection: err %v filter %q", err, f.gotFilter)
	}
}

func assertEngineDown(t *testing.T, obs []Observation, err error) {
	t.Helper()
	if err == nil {
		t.Error("want a non-nil error")
	}
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2: %+v", len(obs), obs)
	}
	for _, m := range []string{"docker.containers_running", "docker.containers_total"} {
		if o := findObs(t, obs, "", m); o.Err == "" {
			t.Errorf("%s has no Err", m)
		}
	}
}

func TestDockerEngineDown(t *testing.T) {
	f := &fakeDockerEngine{listErr: errors.New("docker: cannot reach /x: permission denied")}
	obs, err := newTestDocker(f, nil).Collect(context.Background(), nil)
	assertEngineDown(t, obs, err)
}

func TestDockerEngineFactoryFails(t *testing.T) {
	p := dockerPlugin{engine: func(string) (dockerEngine, error) { return nil, errors.New("no pipes here") }}
	obs, err := p.Collect(context.Background(), nil)
	assertEngineDown(t, obs, err)
}

func TestDockerContainerGoneMidway(t *testing.T) {
	f := basicDockerFake()
	f.inspectErr = map[string]error{"b": errContainerGone}
	obs, err := newTestDocker(f, nil).Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hasObs(obs, "job", "docker.state") {
		t.Error("gone container reported")
	}
	if v := findObs(t, obs, "", "docker.containers_total").Value; *v != 1 {
		t.Errorf("total = %v", *v)
	}
	if v := findObs(t, obs, "", "docker.containers_running").Value; *v != 1 {
		t.Errorf("running = %v", *v)
	}
}

func TestDockerInspectFails(t *testing.T) {
	f := basicDockerFake()
	f.inspectErr = map[string]error{"a": errors.New("boom")}
	obs, err := newTestDocker(f, nil).Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"docker.state", "docker.health", "docker.restart_count", "docker.exit_code"} {
		if o := findObs(t, obs, "web", m); o.Err == "" {
			t.Errorf("web %s has no Err", m)
		}
	}
	if o := findObs(t, obs, "job", "docker.state"); o.Label == nil {
		t.Error("job state not a value")
	}
	if v := findObs(t, obs, "", "docker.containers_total").Value; *v != 2 {
		t.Errorf("total = %v", *v)
	}
	if v := findObs(t, obs, "", "docker.containers_running").Value; *v != 1 {
		t.Errorf("running = %v", *v)
	}
}

func TestDockerReportsEachContainerOnce(t *testing.T) {
	f := basicDockerFake()
	f.containers = append(f.containers, containerSummary{ID: "a", Name: "web2", State: "running"})
	obs, err := newTestDocker(f, nil).Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, o := range obs {
		if o.Metric == "docker.state" {
			n++
		}
		if o.Item == "web2" {
			t.Error("duplicate ID reported under second name")
		}
	}
	if n != 2 {
		t.Errorf("%d state observations, want 2", n)
	}
}

func TestDockerBoundsInspects(t *testing.T) {
	f := &fakeDockerEngine{details: map[string]containerDetail{}, inspectDelay: 10 * time.Millisecond}
	for i := 0; i < 20; i++ {
		id := string(rune('a' + i))
		f.containers = append(f.containers, containerSummary{ID: id, Name: "n" + id, State: "running"})
		f.details[id] = containerDetail{Health: "none"}
	}
	obs, err := newTestDocker(f, nil).Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := findObs(t, obs, "", "docker.containers_total").Value; *v != 20 {
		t.Errorf("total = %v", *v)
	}
	if p := f.peak.Load(); p > 8 || p < 2 {
		t.Errorf("peak inspects = %d, want 2..8", p)
	}
}

func TestDockerDeadContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	obs, err := newTestDocker(basicDockerFake(), nil).Collect(ctx, nil)
	if obs != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("obs %v err %v", obs, err)
	}
}

func TestDockerCollectIsBounded(t *testing.T) {
	f := &fakeDockerEngine{blockUntilDone: true}
	p := newTestDocker(f, nil)
	p.timeout = 100 * time.Millisecond
	start := time.Now()
	obs, err := p.Collect(context.Background(), nil)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Collect took %v", d)
	}
	assertEngineDown(t, obs, err)
}
