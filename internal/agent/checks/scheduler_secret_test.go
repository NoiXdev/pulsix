package checks

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// secretValue is a field value no result may ever carry.
const secretValue = "s3cret-Value-42"

// fakeSecretPlugin echoes the params it was given into its error
// observation and its advisory error, so a test sees at once whether a
// secret's fields reached either.
type fakeSecretPlugin struct {
	mu         sync.Mutex
	plain      int
	calls      int
	lastParams map[string]any
	lastFields map[string]string
	// mutate, when set, writes into the fields map the plugin was handed.
	mutate bool
}

func (p *fakeSecretPlugin) Name() string { return "db" }

func (p *fakeSecretPlugin) Collect(context.Context, map[string]any) ([]Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plain++
	return []Observation{{Metric: "db.up", Value: floatPtr(1)}}, nil
}

func (p *fakeSecretPlugin) CollectWithSecret(_ context.Context, params map[string]any, fields map[string]string) ([]Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.lastParams = maps.Clone(params)
	p.lastFields = maps.Clone(fields)
	if p.mutate && fields != nil {
		fields["password"] = "changed by the plugin"
	}
	echo := fmt.Errorf("params %v", params)
	return []Observation{
		{Metric: "db.up", Value: floatPtr(1)},
		ErrObservation("", "db.echo", echo),
	}, echo
}

func (p *fakeSecretPlugin) SecretErrorMetric() string { return "db.up" }

func (p *fakeSecretPlugin) seen() (plain, calls int, params map[string]any, fields map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.plain, p.calls, p.lastParams, p.lastFields
}

// fakeSecrets is a Secrets whose content a test changes; it hands out its
// own maps, uncopied, so a scheduler that does not copy them is caught.
type fakeSecrets struct {
	mu      sync.Mutex
	secrets map[string]map[string]string
	problem string
}

func (f *fakeSecrets) Secret(name string) (map[string]string, bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fields, ok := f.secrets[name]
	return fields, ok, f.problem
}

func (f *fakeSecrets) set(name string, fields map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.secrets == nil {
		f.secrets = map[string]map[string]string{}
	}
	f.secrets[name] = fields
}

func (f *fakeSecrets) setProblem(p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.problem = p
}

// secretRun runs one check of plugin p with params and returns a function
// that fires its ticker once and waits for the batch it produced.
func secretRun(t *testing.T, p Plugin, params map[string]any, opts ...SchedulerOption) (tick func() []Result) {
	t.Helper()
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"db": p}, time.Now, ticks.factory(), opts...)
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "db", Plugin: "db", Params: params, Interval: time.Minute}}})
	ctx, cancel := context.WithCancel(context.Background())
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run = %v", err)
		}
	})
	return func() []Result {
		t.Helper()
		before := len(sink.all())
		_, tickers := ticks.recorded()
		tickers[0].c <- time.Now()
		waitForSchedulerTest(t, 2*time.Second, func() bool { return len(sink.all()) > before })
		all := sink.all()
		return all[len(all)-1]
	}
}

// assertNoValue fails when any part of a result carries the secret value.
func assertNoValue(t *testing.T, results []Result) {
	t.Helper()
	for _, r := range results {
		text := fmt.Sprintf("%s %s %s %v", r.Item, r.Metric, r.Err, r.Label)
		if r.Label != nil {
			text += *r.Label
		}
		if strings.Contains(text, secretValue) {
			t.Fatalf("a result carries the secret value: %+v", r)
		}
	}
}

func TestSchedulerHandsASecretPluginItsFields(t *testing.T) {
	secrets := &fakeSecrets{}
	secrets.set("pg", map[string]string{"password": secretValue, "user": "monitor"})
	plugin := &fakeSecretPlugin{}
	params := map[string]any{"secret": "pg", "database": "shop"}
	tick := secretRun(t, plugin, params, WithSecrets(secrets))

	results := tick()
	plain, calls, gotParams, gotFields := plugin.seen()
	if plain != 0 || calls != 1 {
		t.Fatalf("Collect calls = %d, CollectWithSecret calls = %d; want 0 and 1", plain, calls)
	}
	if want := map[string]string{"password": secretValue, "user": "monitor"}; !reflect.DeepEqual(gotFields, want) {
		t.Fatalf("fields = %v, want %v", gotFields, want)
	}
	// The params are the check's own: nothing added, nothing removed.
	if want := map[string]any{"secret": "pg", "database": "shop"}; !reflect.DeepEqual(gotParams, want) || !reflect.DeepEqual(params, want) {
		t.Fatalf("params = %v (spec %v), want %v", gotParams, params, want)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want the plugin's two", results)
	}
	// The plugin echoed its params into an error observation: no value.
	assertNoValue(t, results)
}

// TestSchedulerHandsTheFieldsAsACopy: a plugin writing into its fields map
// changes nothing for the next round or for the source.
func TestSchedulerHandsTheFieldsAsACopy(t *testing.T) {
	secrets := &fakeSecrets{}
	secrets.set("pg", map[string]string{"password": secretValue})
	plugin := &fakeSecretPlugin{mutate: true}
	tick := secretRun(t, plugin, map[string]any{"secret": "pg"}, WithSecrets(secrets))
	tick()
	tick()
	if _, _, _, fields := plugin.seen(); fields["password"] != secretValue {
		t.Fatalf("second round's fields = %v, want the original", fields)
	}
	if f, _, _ := secrets.Secret("pg"); f["password"] != secretValue {
		t.Fatalf("the source was changed: %v", f)
	}
}

// TestSchedulerReportsAnUndefinedSecretEveryRound: one error observation
// on the plugin's host-level metric, every round, until the secret is
// defined; the plugin is not called meanwhile.
func TestSchedulerReportsAnUndefinedSecretEveryRound(t *testing.T) {
	secrets := &fakeSecrets{}
	plugin := &fakeSecretPlugin{}
	tick := secretRun(t, plugin, map[string]any{"secret": "pg"}, WithSecrets(secrets))
	want := []Result{{CheckKey: "db", Item: "", Metric: "db.up", Err: "secret `pg` is not defined in agent.yaml"}}
	for round := 0; round < 2; round++ {
		got := tick()
		for i := range got {
			got[i].At = time.Time{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: results = %+v, want %+v", round, got, want)
		}
	}
	if plain, calls, _, _ := plugin.seen(); plain != 0 || calls != 0 {
		t.Fatalf("the plugin was called (%d, %d) without its secret", plain, calls)
	}
	secrets.set("pg", map[string]string{"password": secretValue})
	assertNoValue(t, tick())
	if _, calls, _, _ := plugin.seen(); calls != 1 {
		t.Fatalf("CollectWithSecret calls after the secret appeared = %d, want 1", calls)
	}
}

// TestSchedulerNamesWhyTheFileHasNoSecret: when the file is missing or
// unusable, the error says so after the fixed text.
func TestSchedulerNamesWhyTheFileHasNoSecret(t *testing.T) {
	secrets := &fakeSecrets{}
	secrets.setProblem("the file is not usable: /etc/pulsix/agent.yaml is accessible to mode 0644; restrict it")
	tick := secretRun(t, &fakeSecretPlugin{}, map[string]any{"secret": "pg"}, WithSecrets(secrets))
	got := tick()
	want := "secret `pg` is not defined in agent.yaml (the file is not usable: /etc/pulsix/agent.yaml is accessible to mode 0644; restrict it)"
	if len(got) != 1 || got[0].Err != want {
		t.Fatalf("results = %+v, want one error %q", got, want)
	}
}

// TestSchedulerWithoutASecretParam: the plugin is still called through
// CollectWithSecret, with no fields; it decides what that means.
func TestSchedulerWithoutASecretParam(t *testing.T) {
	for _, params := range []map[string]any{nil, {"database": "shop"}, {"secret": nil}} {
		plugin := &fakeSecretPlugin{}
		secrets := &fakeSecrets{}
		secrets.set("pg", map[string]string{"password": secretValue})
		tick := secretRun(t, plugin, params, WithSecrets(secrets))
		tick()
		plain, calls, _, fields := plugin.seen()
		if plain != 0 || calls != 1 || fields != nil {
			t.Fatalf("params %v: Collect %d, CollectWithSecret %d, fields %v; want 0, 1, nil", params, plain, calls, fields)
		}
	}
}

func TestSchedulerRefusesASecretParamThatIsNotAString(t *testing.T) {
	plugin := &fakeSecretPlugin{}
	tick := secretRun(t, plugin, map[string]any{"secret": []any{"pg"}}, WithSecrets(&fakeSecrets{}))
	got := tick()
	if len(got) != 1 || got[0].Metric != "db.up" || got[0].Err != `param "secret" must be a string` {
		t.Fatalf("results = %+v", got)
	}
	if _, calls, _, _ := plugin.seen(); calls != 0 {
		t.Fatal("the plugin was called")
	}
}

// TestSchedulerWithoutSecrets: a scheduler nobody gave a secrets source
// knows no secret.
func TestSchedulerWithoutSecrets(t *testing.T) {
	tick := secretRun(t, &fakeSecretPlugin{}, map[string]any{"secret": "pg"})
	got := tick()
	if len(got) != 1 || got[0].Err != "secret `pg` is not defined in agent.yaml" {
		t.Fatalf("results = %+v", got)
	}
}

// statefulSecretPlugin hands every check key a fresh fakeSecretPlugin.
type statefulSecretPlugin struct {
	fakeSecretPlugin
	mu        sync.Mutex
	instances []*fakeSecretPlugin
}

func (p *statefulSecretPlugin) NewInstance() Plugin {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst := &fakeSecretPlugin{}
	p.instances = append(p.instances, inst)
	return inst
}

func TestSchedulerStatefulSecretPlugin(t *testing.T) {
	secrets := &fakeSecrets{}
	secrets.set("pg", map[string]string{"password": secretValue})
	proto := &statefulSecretPlugin{}
	tick := secretRun(t, proto, map[string]any{"secret": "pg"}, WithSecrets(secrets))
	tick()
	proto.mu.Lock()
	inst := proto.instances
	proto.mu.Unlock()
	if len(inst) != 1 {
		t.Fatalf("instances = %d, want 1", len(inst))
	}
	if _, calls, _, fields := inst[0].seen(); calls != 1 || fields["password"] != secretValue {
		t.Fatalf("instance calls = %d, fields = %v", calls, fields)
	}
}

// panicSecretPlugin panics in CollectWithSecret with a planted value.
type panicSecretPlugin struct{ fakeSecretPlugin }

func (p *panicSecretPlugin) CollectWithSecret(context.Context, map[string]any, map[string]string) ([]Observation, error) {
	panic("boom " + secretValue)
}

func TestSecretPluginPanicIsRecovered(t *testing.T) {
	fs := &fakeSecrets{}
	fs.set("main", map[string]string{"password": secretValue})
	tick := secretRun(t, &panicSecretPlugin{}, map[string]any{SecretParam: "main"}, WithSecrets(fs))
	res := tick()
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(res), res)
	}
	r := res[0]
	if r.Item != "" || r.Metric != "db.up" || r.Err != "db: collection failed unexpectedly" {
		t.Errorf("result = %+v", r)
	}
	if strings.Contains(fmt.Sprintf("%+v", res), secretValue) {
		t.Error("panic value leaked into the result")
	}
}
