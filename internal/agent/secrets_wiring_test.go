// Tests for the secrets file in the running agent (backlog 3.5): every
// heartbeat reports the loaded names and the file's state, the file is
// read again on the heartbeat at most once a minute, and the scheduler
// hands a SecretPlugin its secret — with no field value ever reaching
// the stream or the log.
package agent

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/secrets"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// secretValue is a field value that must never leave the agent.
const secretValue = "s3cret-Value-42"

const twoSecrets = "secrets:\n  pg_main:\n    password: " + secretValue + "\n  mysql_shop:\n    user: monitor\n"

func writeSecretsFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertNothingSent fails when any message the agent sent, or its log,
// carries the secret value.
func assertNothingSent(t *testing.T, stream *fakeStream, logs string) {
	t.Helper()
	for _, m := range stream.sentMessages() {
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatalf("marshal %v: %v", m, err)
		}
		if bytes.Contains(b, []byte(secretValue)) {
			t.Fatalf("a sent message carries the secret value: %v", m)
		}
	}
	if strings.Contains(logs, secretValue) {
		t.Fatalf("the log carries the secret value:\n%s", logs)
	}
}

func (lt *loopTest) logText() string {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.logs.String()
}

func TestLoopHeartbeatReportsSecretNamesAndState(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	writeSecretsFile(t, lt.cfg.SecretsPath, twoSecrets)
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}

	cancel, done := runLoop(t, lt.newLoop())
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	hb := heartbeats(stream)[0]
	if got, want := hb.GetSecretNames(), []string{"mysql_shop", "pg_main"}; !reflect.DeepEqual(got, want) {
		t.Errorf("secret_names = %v, want %v", got, want)
	}
	if got := hb.GetSecretsState(); got != "ok" {
		t.Errorf("secrets_state = %q, want ok", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	assertNothingSent(t, stream, lt.logText())
	if !strings.Contains(lt.logText(), "loaded the secrets file") {
		t.Errorf("log = %s, want the loaded line at start", lt.logText())
	}
}

func TestLoopHeartbeatReportsAnAbsentSecretsFile(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}

	cancel, done := runLoop(t, lt.newLoop())
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	hb := heartbeats(stream)[0]
	if len(hb.GetSecretNames()) != 0 || hb.GetSecretsState() != "absent" {
		t.Errorf("heartbeat = %v, want no names and state absent", hb)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v", err)
	}
}

// TestLoopHeartbeatReportsARefusedSecretsFile: the agent's own protection
// rule refuses the file, so no name is reported, only the reason.
func TestLoopHeartbeatReportsARefusedSecretsFile(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	writeSecretsFile(t, lt.cfg.SecretsPath, twoSecrets)
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	opts := append(lt.options(), withProtector(&fakeProtector{loose: map[string]string{lt.cfg.SecretsPath: "mode 0644"}}))

	cancel, done := runLoop(t, NewLoop(lt.cfg, lt.dialer, lt.facts, opts...))
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	hb := heartbeats(stream)[0]
	want := "configuration file " + lt.cfg.SecretsPath + " holds secrets or commands but is accessible to mode 0644; restrict it"
	if len(hb.GetSecretNames()) != 0 || hb.GetSecretsState() != want {
		t.Errorf("heartbeat = %v, want no names and state %q", hb, want)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v", err)
	}
	assertNothingSent(t, stream, lt.logText())
}

// TestLoopStartsWithARefusedSecretsFileInTheDataDir: where the protector
// checks every entry of the data directory (Windows), the secrets file is
// left to its watcher, which applies the full rule on every read. A
// too-open or user-owned agent.yaml must not stop the agent — neither at
// start nor on the enrollment path — it keeps running without secrets
// and reports why.
func TestLoopStartsWithARefusedSecretsFileInTheDataDir(t *testing.T) {
	for _, enrolled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enrolled=%v", enrolled), func(t *testing.T) {
			lt := newLoopTest(t)
			if enrolled {
				lt.withIdentity()
			} else {
				caInst := newTestCA(t)
				lt.withToken(caInst)
				lt.dialer.enrollConn = &fakeEnrollConn{respond: enrollResponder(caInst, nil, nil)}
			}
			writeSecretsFile(t, lt.cfg.SecretsPath, twoSecrets)
			stream := newFakeStream()
			lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
			p := &fakeProtector{
				entries: true,
				loose:   map[string]string{lt.cfg.SecretsPath: `owner PC\mallory, BUILTIN\Users`},
				owner:   map[string]string{lt.cfg.SecretsPath: `owner PC\mallory`},
			}
			opts := append(lt.options(), withProtector(p))

			cancel, done := runLoop(t, NewLoop(lt.cfg, lt.dialer, lt.facts, opts...))
			waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
			hb := heartbeats(stream)[0]
			want := "configuration file " + lt.cfg.SecretsPath + ` holds secrets or commands but is accessible to owner PC\mallory, BUILTIN\Users; restrict it`
			if len(hb.GetSecretNames()) != 0 || hb.GetSecretsState() != want {
				t.Errorf("heartbeat = %v, want no names and state %q", hb, want)
			}
			cancel()
			if err := <-done; err != nil {
				t.Errorf("Run = %v", err)
			}
			assertNothingSent(t, stream, lt.logText())
		})
	}
}

// TestLoopRereadsTheSecretsFileOnTheHeartbeat: a heartbeat a minute later
// reports the file as it is now.
func TestLoopRereadsTheSecretsFileOnTheHeartbeat(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	var clockMu sync.Mutex
	now := time.Now()
	lt.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	writeSecretsFile(t, lt.cfg.SecretsPath, twoSecrets)
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}

	cancel, done := runLoop(t, lt.newLoop())
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	writeSecretsFile(t, lt.cfg.SecretsPath, "secrets:\n  redis:\n    password: x\n")
	_, tickers := lt.waitForTickers(1)

	// Within the minute: still the names read at start.
	before := len(heartbeats(stream))
	tickers[0].c <- time.Now()
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > before })
	if got := heartbeats(stream)[before].GetSecretNames(); len(got) != 2 {
		t.Fatalf("secret_names within the minute = %v, want the two read at start", got)
	}

	clockMu.Lock()
	now = now.Add(time.Minute)
	clockMu.Unlock()
	before = len(heartbeats(stream))
	tickers[0].c <- time.Now()
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > before })
	if got := heartbeats(stream)[before].GetSecretNames(); !reflect.DeepEqual(got, []string{"redis"}) {
		t.Fatalf("secret_names a minute later = %v, want [redis]", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v", err)
	}
}

// echoSecretPlugin records the fields it was handed and echoes its params
// into an error observation.
type echoSecretPlugin struct {
	mu     sync.Mutex
	fields []map[string]string
}

func (p *echoSecretPlugin) Name() string { return "db" }

func (p *echoSecretPlugin) Collect(context.Context, map[string]any) ([]checks.Observation, error) {
	return nil, nil
}

func (p *echoSecretPlugin) CollectWithSecret(_ context.Context, params map[string]any, fields map[string]string) ([]checks.Observation, error) {
	p.mu.Lock()
	p.fields = append(p.fields, maps.Clone(fields))
	p.mu.Unlock()
	echo := fmt.Errorf("params %v", params)
	return []checks.Observation{checks.ValueObservation("", "db.up", 1), checks.ErrObservation("", "db.echo", echo)}, echo
}

func (p *echoSecretPlugin) SecretErrorMetric() string { return "db.up" }

func (p *echoSecretPlugin) handed() []map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]string(nil), p.fields...)
}

// TestLoopHandsAPluginItsSecret: end to end, a check naming a secret gets
// its fields, a check naming one the file lacks reports the error on the
// stream, and the value never travels.
func TestLoopHandsAPluginItsSecret(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	writeSecretsFile(t, lt.cfg.SecretsPath, twoSecrets)
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	plugin := &echoSecretPlugin{}
	opts := append(lt.options(), withRegistry(map[string]checks.Plugin{"db": plugin}))

	cancel, done := runLoop(t, NewLoop(lt.cfg, lt.dialer, lt.facts, opts...))
	waitFor(t, 2*time.Second, func() bool { return len(stream.sentMessages()) > 0 })
	stream.pushManagerMessage(&agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{
		Version: "cfg-v1",
		Checks: []*agentpb.CheckSpec{
			{Key: "a", Plugin: "db", ParamsJson: `{"secret":"pg_main"}`, IntervalSeconds: 30},
		},
	}})
	waitFor(t, 2*time.Second, func() bool { _, tk := lt.ticks.recorded(); return len(tk) == 4 })
	stream.pushManagerMessage(&agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{
		Version: "cfg-v2",
		Checks: []*agentpb.CheckSpec{
			{Key: "a", Plugin: "db", ParamsJson: `{"secret":"pg_main"}`, IntervalSeconds: 30},
			{Key: "b", Plugin: "db", ParamsJson: `{"secret":"nope"}`, IntervalSeconds: 30},
		},
	}})
	waitFor(t, 2*time.Second, func() bool { _, tk := lt.ticks.recorded(); return len(tk) == 5 })
	_, tickers := lt.ticks.recorded()
	tickers[3].c <- time.Now()
	waitFor(t, 2*time.Second, func() bool { return len(checkResultMessages(stream)) == 1 })
	tickers[4].c <- time.Now()
	waitFor(t, 2*time.Second, func() bool { return len(checkResultMessages(stream)) == 2 })

	if got := plugin.handed(); len(got) != 1 || !reflect.DeepEqual(got[0], map[string]string{"password": secretValue}) {
		t.Fatalf("fields handed = %v, want pg_main's once", got)
	}
	b := checkResultMessages(stream)[1].GetResults()
	if len(b) != 1 || b[0].GetCheckKey() != "b" || b[0].GetMetric() != "db.up" ||
		b[0].GetError() != "secret `nope` is not defined in agent.yaml" {
		t.Fatalf("results of b = %v, want the not-defined error on db.up", b)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v", err)
	}
	assertNothingSent(t, stream, lt.logText())
}

func TestSecretsSourceSaysWhyASecretIsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	refuse := false
	now := time.Now()
	clock := func() time.Time { return now }
	w := secrets.NewWatcher(path, func(p string) error {
		if refuse {
			return fmt.Errorf("configuration file %s holds secrets or commands but is accessible to mode 0644; restrict it", p)
		}
		return nil
	}, secrets.WithClock(clock))
	src := secretsSource{w}

	if _, ok, problem := src.Secret("pg"); ok || problem != "the file "+path+" does not exist" {
		t.Fatalf("absent: ok %v, problem %q", ok, problem)
	}
	writeSecretsFile(t, path, "secrets:\n  pg:\n    password: "+secretValue+"\n")
	now = now.Add(time.Minute)
	if fields, ok, problem := src.Secret("pg"); !ok || problem != "" || fields["password"] != secretValue {
		t.Fatalf("defined: %v, %v, %q", fields, ok, problem)
	}
	if _, ok, problem := src.Secret("other"); ok || problem != "" {
		t.Fatalf("not defined in a good file: ok %v, problem %q", ok, problem)
	}
	refuse = true
	now = now.Add(time.Minute)
	want := "the file is not usable: configuration file " + path + " holds secrets or commands but is accessible to mode 0644; restrict it"
	if _, ok, problem := src.Secret("pg"); ok || problem != want {
		t.Fatalf("refused: ok %v, problem %q, want %q", ok, problem, want)
	}
}

// commandArg is a command argument that must never leave the agent.
const commandArg = "arg-Pa55word-77"

// commandProgram is an absolute program path on the agent's OS, which the
// file's parser requires.
func commandProgram() string {
	if runtime.GOOS == "windows" {
		return `C:\Program Files\Pulsix\check_disk.exe`
	}
	return "/usr/lib/check_disk"
}

// commandsFile is a commands section defining disk as commandProgram with
// commandArg; the program is single-quoted so a Windows path's
// backslashes are taken as written.
func commandsFile() string {
	return "commands:\n  disk: ['" + commandProgram() + "', -p, " + commandArg + "]\n"
}

func TestCommandsSourceSaysWhyACommandIsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	refuse := false
	now := time.Now()
	clock := func() time.Time { return now }
	w := secrets.NewWatcher(path, func(p string) error {
		if refuse {
			return fmt.Errorf("configuration file %s holds secrets or commands but is accessible to mode 0644; restrict it", p)
		}
		return nil
	}, secrets.WithClock(clock))
	src := commandsSource{w}

	if argv, ok, problem := src.Command("disk"); ok || argv != nil || problem != "the file "+path+" does not exist" {
		t.Fatalf("absent: %q, ok %v, problem %q", argv, ok, problem)
	}
	writeSecretsFile(t, path, commandsFile())
	now = now.Add(time.Minute)
	want := []string{commandProgram(), "-p", commandArg}
	if argv, ok, problem := src.Command("disk"); !ok || problem != "" || !reflect.DeepEqual(argv, want) {
		t.Fatalf("defined: %q, %v, %q; want %q", argv, ok, problem, want)
	}
	if argv, ok, problem := src.Command("other"); ok || argv != nil || problem != "" {
		t.Fatalf("not defined in a good file: %q, ok %v, problem %q", argv, ok, problem)
	}
	refuse = true
	now = now.Add(time.Minute)
	wantProblem := "the file is not usable: configuration file " + path + " holds secrets or commands but is accessible to mode 0644; restrict it"
	if argv, ok, problem := src.Command("disk"); ok || argv != nil || problem != wantProblem {
		t.Fatalf("refused: %q, ok %v, problem %q, want %q", argv, ok, problem, wantProblem)
	}
	// The secrets source names the same problem.
	if _, _, problem := (secretsSource{w}).Secret("pg"); problem != wantProblem {
		t.Fatalf("secretsSource problem = %q, want %q", problem, wantProblem)
	}
}

// echoCommandPlugin records the argv its check's command resolves to and
// reports only the command's name.
type echoCommandPlugin struct {
	mu    sync.Mutex
	argvs [][]string
}

func (p *echoCommandPlugin) Name() string { return "script" }

func (p *echoCommandPlugin) Collect(context.Context, map[string]any) ([]checks.Observation, error) {
	return nil, nil
}

func (p *echoCommandPlugin) CollectWithCommands(_ context.Context, params map[string]any, commands checks.Commands) ([]checks.Observation, error) {
	name, _ := params["command"].(string)
	argv, ok, problem := commands.Command(name)
	p.mu.Lock()
	p.argvs = append(p.argvs, argv)
	p.mu.Unlock()
	if !ok {
		return []checks.Observation{checks.ErrObservation(name, "script.status", fmt.Errorf("not defined (%s)", problem))}, nil
	}
	return []checks.Observation{checks.ValueObservation(name, "script.exit_code", 0)}, nil
}

func (p *echoCommandPlugin) handed() [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]string(nil), p.argvs...)
}

// TestLoopHandsAPluginTheHostsCommands: end to end, a command plugin looks
// its command up in the same file the secrets come from, and no argument
// reaches the stream or the log.
func TestLoopHandsAPluginTheHostsCommands(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	writeSecretsFile(t, lt.cfg.SecretsPath, twoSecrets+commandsFile())
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	plugin := &echoCommandPlugin{}
	opts := append(lt.options(), withRegistry(map[string]checks.Plugin{"script": plugin}))

	cancel, done := runLoop(t, NewLoop(lt.cfg, lt.dialer, lt.facts, opts...))
	waitFor(t, 2*time.Second, func() bool { return len(stream.sentMessages()) > 0 })
	stream.pushManagerMessage(&agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{
		Version: "cfg-v1",
		Checks: []*agentpb.CheckSpec{
			{Key: "a", Plugin: "script", ParamsJson: `{"command":"disk"}`, IntervalSeconds: 30},
		},
	}})
	waitFor(t, 2*time.Second, func() bool { _, tk := lt.ticks.recorded(); return len(tk) == 4 })
	_, tickers := lt.ticks.recorded()
	tickers[3].c <- time.Now()
	waitFor(t, 2*time.Second, func() bool { return len(checkResultMessages(stream)) == 1 })

	if got, want := plugin.handed(), [][]string{{commandProgram(), "-p", commandArg}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv handed = %q, want %q", got, want)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v", err)
	}
	for _, m := range stream.sentMessages() {
		if strings.Contains(m.String(), commandArg) {
			t.Fatalf("a sent message carries a command argument: %v", m)
		}
	}
	if strings.Contains(lt.logText(), commandArg) {
		t.Fatalf("the log carries a command argument:\n%s", lt.logText())
	}
}

// TestReportedSecretsState: the state travels as a proto3 string, which
// must be valid UTF-8 or the whole heartbeat fails to encode, and is cut
// to a bound.
func TestReportedSecretsState(t *testing.T) {
	tests := []struct {
		in   secrets.State
		want string
	}{
		{secrets.StateOK, "ok"},
		{secrets.StateAbsent, "absent"},
		{secrets.State("/etc/pulsix/\xffagent.yaml: line 3, column 5: x"), "/etc/pulsix/�agent.yaml: line 3, column 5: x"},
	}
	for _, tt := range tests {
		if got := reportedSecretsState(tt.in); got != tt.want {
			t.Errorf("reportedSecretsState(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	long := reportedSecretsState(secrets.State(strings.Repeat("é", 2000)))
	if len(long) > maxSecretsStateLen || !utf8.ValidString(long) {
		t.Fatalf("long state: %d bytes, valid %v", len(long), utf8.ValidString(long))
	}
}
