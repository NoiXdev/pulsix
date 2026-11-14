package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

var _ checks.CommandPlugin = (*scriptPlugin)(nil)

func TestName(t *testing.T) {
	if got := New().Name(); got != "script" {
		t.Errorf("Name() = %q, want script", got)
	}
}

func TestCollectExitCodes(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{0, "ok"}, {1, "warn"}, {2, "crit"}, {3, "unknown"}, {4, "unknown"}, {255, "unknown"}, {-1, "unknown"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.code), func(t *testing.T) {
			p := newPlugin(exitedWith(tt.code, "CHECK - fine\n"), progOK())
			got := collect(t, p, map[string]any{"commands": []string{"x"}}, oneCommand())
			equalLines(t, got, []string{
				`x script.status "` + tt.want + `"`,
				`x script.output "CHECK - fine"`,
				fmt.Sprintf("x script.exit_code %d", tt.code),
				"x script.duration_seconds 0.25",
			})
		})
	}
}

func TestCollectNotExited(t *testing.T) {
	r := &fakeRunner{run: func(context.Context, []string) (runResult, error) {
		return runResult{stdout: []byte("OK | a=1"), duration: durationOfRun}, nil
	}}
	got := collect(t, newPlugin(r, progOK()), map[string]any{"commands": []string{"x"}}, oneCommand())
	equalLines(t, got, []string{
		`x script.status "unknown"`,
		`x script.output "` + textNoExit + `"`,
		"x script.duration_seconds 0.25",
	})
}

func TestCollectNagiosValues(t *testing.T) {
	r := exitedWith(1, "DISK WARNING - free space: / 1 GB | /=1GB;2;3 'free %'=12%\n")
	got := collect(t, newPlugin(r, progOK()), map[string]any{"commands": []any{"x"}, "format": "nagios"}, oneCommand())
	equalLines(t, got, []string{
		`x script.status "warn"`,
		`x script.output "DISK WARNING - free space: / 1 GB"`,
		"x script.exit_code 1",
		"x script.duration_seconds 0.25",
		"x:/ script.value 1073741824",
		"x:free % script.value 12",
	})
}

func TestCollectNagiosByteOrderMark(t *testing.T) {
	r := exitedWith(0, "\xEF\xBB\xBF'a b'=1 | c=2\n")
	got := collect(t, newPlugin(r, progOK()), map[string]any{"commands": []string{"x"}}, oneCommand())
	equalLines(t, got, []string{
		`x script.status "ok"`,
		`x script.output "'a b'=1"`,
		"x script.exit_code 0",
		"x script.duration_seconds 0.25",
		"x:c script.value 2",
	})
}

func TestCollectJSON(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		r := exitedWith(2, `{"status":"ok","message":"fine","metrics":{"age":2}}`)
		got := collect(t, newPlugin(r, progOK()), map[string]any{"commands": []string{"x"}, "format": "json"}, oneCommand())
		equalLines(t, got, []string{
			`x script.status "ok"`,
			`x script.output "fine"`,
			"x script.exit_code 2",
			"x script.duration_seconds 0.25",
			"x:age script.value 2",
		})
	})
	t.Run("refused output never shows the script's text", func(t *testing.T) {
		r := exitedWith(0, `{"message":"MARKER","metrics":{"a":1}}`)
		got := collect(t, newPlugin(r, progOK()), map[string]any{"commands": []string{"x"}, "format": "json"}, oneCommand())
		equalLines(t, got, []string{
			`x script.status "unknown"`,
			`x script.output "` + reasonNoStatus + `"`,
			"x script.exit_code 0",
			"x script.duration_seconds 0.25",
		})
	})
	t.Run("a byte order mark is dropped", func(t *testing.T) {
		r := exitedWith(0, "\xEF\xBB\xBF"+`{"status":"warn","message":"bom"}`)
		got := collect(t, newPlugin(r, progOK()), map[string]any{"commands": []string{"x"}, "format": "json"}, oneCommand())
		equalLines(t, got[:2], []string{`x script.status "warn"`, `x script.output "bom"`})
	})
	t.Run("empty message", func(t *testing.T) {
		r := exitedWith(0, `{"status":"ok"}`)
		got := collect(t, newPlugin(r, progOK()), map[string]any{"commands": []string{"x"}, "format": "json"}, oneCommand())
		if got[1] != `x script.output "(no output)"` {
			t.Errorf("output = %s, want (no output)", got[1])
		}
	})
}

func TestCollectWithoutARun(t *testing.T) {
	refusal := refuse("writable by group or others")
	tests := []struct {
		name    string
		cmds    fakeCommands
		files   map[string]error
		run     func(context.Context, []string) (runResult, error)
		timeout any
		want    []string
	}{
		{
			name: "undefined", cmds: fakeCommands{},
			want: []string{`x script.status "unknown"`, `x script.output "command \"x\" is not defined on this host"`},
		},
		{
			name: "undefined with the file's problem", cmds: fakeCommands{problem: "agent.yaml is missing"},
			want: []string{`x script.status "unknown"`, `x script.output "command \"x\" is not defined on this host (agent.yaml is missing)"`},
		},
		{
			name: "program refused", cmds: oneCommand("-w", "1"), files: map[string]error{prog: refusal},
			want: []string{`x script.status "unknown"`, `x script.output "` + prog + `: writable by group or others"`},
		},
		{
			name: "program missing", cmds: oneCommand(), files: map[string]error{},
			want: []string{`x script.status "unknown"`, `x script.output "` + prog + `: ` + errNotFile.Error() + `"`},
		},
		{
			name: "script argument refused", cmds: oneCommand("-f", "/opt/x.py", "/opt/y.py"),
			files: map[string]error{prog: nil, "/opt/x.py": refusal, "/opt/y.py": errors.New("other")},
			want:  []string{`x script.status "unknown"`, `x script.output "/opt/x.py: writable by group or others"`},
		},
		{
			name: "program could not be checked", cmds: oneCommand(), files: map[string]error{prog: errors.New("lstat failed: EIO")},
			want: []string{`x script.status "unknown"`, `x script.output "` + prog + `: ` + textUncheckedProgram + `"`},
		},
		{
			name: "argument could not be checked", cmds: oneCommand("/opt/" + marker),
			files: map[string]error{prog: nil, "/opt/" + marker: errors.New("stat /opt/" + marker + ": permission denied")},
			want:  []string{`x script.status "unknown"`, `x script.output "` + textUncheckedArgument + `"`},
		},
		{
			name: "relative program", cmds: fakeCommands{argv: map[string][]string{"x": {"check_x", "MARKER"}}},
			want: []string{`x script.status "unknown"`, `x script.output "check_x: not an absolute path"`},
		},
		{
			name: "busy", cmds: oneCommand(), files: map[string]error{prog: nil},
			run:  func(context.Context, []string) (runResult, error) { return runResult{}, errBusy },
			want: []string{`x script.status "unknown"`, `x script.output "an earlier run of this command has not exited yet"`},
		},
		{
			name: "start failure with an errno", cmds: oneCommand(), files: map[string]error{prog: nil},
			run: func(context.Context, []string) (runResult, error) {
				return runResult{}, fmt.Errorf("fork/exec %s: %w", prog, syscall.ENOEXEC)
			},
			want: []string{`x script.status "unknown"`, `x script.output "` + textStartFailed + `: ` + syscall.ENOEXEC.Error() + `"`},
		},
		{
			name: "start failure without an errno", cmds: oneCommand(), files: map[string]error{prog: nil},
			run:  func(context.Context, []string) (runResult, error) { return runResult{}, errors.New("odd") },
			want: []string{`x script.status "unknown"`, `x script.output "` + textStartFailed + `"`},
		},
		{
			name: "timeout", cmds: oneCommand(), files: map[string]error{prog: nil}, timeout: 7,
			run: func(context.Context, []string) (runResult, error) {
				return runResult{duration: 7 * time.Second}, errTimeout
			},
			want: []string{`x script.status "unknown"`, `x script.output "timed out after 7s"`, "x script.duration_seconds 7"},
		},
		{
			name: "timeout without a duration reports the bound", cmds: oneCommand(), files: map[string]error{prog: nil}, timeout: 3.0,
			run: func(context.Context, []string) (runResult, error) {
				return runResult{exitCode: 9}, errTimeout
			},
			want: []string{`x script.status "unknown"`, `x script.output "timed out after 3s"`, "x script.duration_seconds 3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{run: tt.run}
			if r.run == nil {
				r.run = func(context.Context, []string) (runResult, error) {
					t.Error("the runner was called")
					return runResult{}, nil
				}
			}
			params := map[string]any{"commands": []string{"x"}}
			if tt.timeout != nil {
				params["timeout_seconds"] = tt.timeout
			}
			got := collect(t, newPlugin(r, &fakeChecker{files: tt.files}), params, tt.cmds)
			equalLines(t, got, tt.want)
		})
	}
}

func TestCollectChecksAbsolutePathArguments(t *testing.T) {
	r := exitedWith(0, "OK")
	c := &fakeChecker{files: map[string]error{prog: nil, "/opt/x.py": nil}}
	cmds := oneCommand("-p", "/", "/opt/x.py", "relative.py", "--file=/opt/z")
	got := collect(t, newPlugin(r, c), map[string]any{"commands": []string{"x"}}, cmds)
	if got[0] != `x script.status "ok"` {
		t.Fatalf("status = %s, want ok", got[0])
	}
	want := []string{prog, "/", "/opt/x.py"}
	if fmt.Sprint(c.checked) != fmt.Sprint(want) {
		t.Errorf("checked %q, want %q", c.checked, want)
	}
	if calls := r.called(); len(calls) != 1 || fmt.Sprint(calls[0]) != fmt.Sprint(append([]string{prog}, "-p", "/", "/opt/x.py", "relative.py", "--file=/opt/z")) {
		t.Errorf("runner called with %q", calls)
	}
}

func TestCollectTimeoutOnTheRunnersContext(t *testing.T) {
	tests := []struct {
		timeout any
		want    time.Duration
	}{
		{nil, 10 * time.Second},
		{3, 3 * time.Second},
		{int64(60), 60 * time.Second},
		{float64(1), time.Second},
		{int32(5), 5 * time.Second},
		{json.Number("20"), 20 * time.Second},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.timeout), func(t *testing.T) {
			var left time.Duration
			r := &fakeRunner{run: func(ctx context.Context, _ []string) (runResult, error) {
				d, ok := ctx.Deadline()
				if !ok {
					t.Error("the runner's context has no deadline")
				}
				left = time.Until(d)
				return runResult{exited: true}, nil
			}}
			params := map[string]any{"commands": []string{"x"}}
			if tt.timeout != nil {
				params["timeout_seconds"] = tt.timeout
			}
			collect(t, newPlugin(r, progOK()), params, oneCommand())
			if left > tt.want || left < tt.want-5*time.Second {
				t.Errorf("deadline in %s, want about %s", left, tt.want)
			}
		})
	}
}

func TestCollectInvalidParams(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"unknown format", map[string]any{"format": "xml"}, textBadFormat},
		{"format not a string", map[string]any{"format": 1}, textBadFormat},
		{"timeout zero", map[string]any{"timeout_seconds": 0}, textBadTimeout},
		{"timeout too long", map[string]any{"timeout_seconds": 61}, textBadTimeout},
		{"timeout fractional", map[string]any{"timeout_seconds": 1.5}, textBadTimeout},
		{"timeout a string", map[string]any{"timeout_seconds": "10"}, textBadTimeout},
		{"timeout a fractional JSON number", map[string]any{"timeout_seconds": json.Number("1.5")}, textBadTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.params["commands"] = []string{"x", "y"}
			r := &fakeRunner{run: func(context.Context, []string) (runResult, error) {
				t.Error("the runner was called")
				return runResult{}, nil
			}}
			got := collect(t, newPlugin(r, progOK()), tt.params, oneCommand())
			equalLines(t, got, []string{
				`x script.status "unknown"`, "x script.output " + strconv.Quote(tt.want),
				`y script.status "unknown"`, "y script.output " + strconv.Quote(tt.want),
			})
		})
	}
}

func TestCollectCommandsParam(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		obs, err := newPlugin(exitedWith(0, ""), progOK()).CollectWithCommands(context.Background(), map[string]any{}, oneCommand())
		if obs != nil || err != nil {
			t.Errorf("got (%v, %v), want nothing", obs, err)
		}
	})
	t.Run("not a list of strings", func(t *testing.T) {
		obs, err := newPlugin(exitedWith(0, ""), progOK()).CollectWithCommands(context.Background(), map[string]any{"commands": []any{"x", 1}}, oneCommand())
		if obs != nil || err == nil {
			t.Errorf("got (%v, %v), want an error and nothing", obs, err)
		}
	})
	t.Run("duplicates, empty and unreportable names, in order", func(t *testing.T) {
		cmds := fakeCommands{argv: map[string][]string{"b": {prog}, "a": {prog}}}
		got := collect(t, newPlugin(exitedWith(0, "OK"), progOK()), map[string]any{"commands": []any{"b", "", "a", "b", "bad\x00"}}, cmds)
		var statuses []string
		for _, l := range got {
			if strings.Contains(l, "script.status") {
				statuses = append(statuses, l)
			}
		}
		equalLines(t, statuses, []string{`b script.status "ok"`, `a script.status "ok"`})
	})
	t.Run("more than the bound", func(t *testing.T) {
		names := make([]string, maxCommands+2)
		argv := map[string][]string{}
		for i := range names {
			names[i] = fmt.Sprintf("c%02d", i)
			argv[names[i]] = []string{prog}
		}
		r := exitedWith(0, "OK")
		got := collect(t, newPlugin(r, progOK()), map[string]any{"commands": names}, fakeCommands{argv: argv})
		if n := len(r.called()); n != maxCommands {
			t.Errorf("%d runs, want %d", n, maxCommands)
		}
		tail := got[len(got)-4:]
		equalLines(t, tail, []string{
			`c32 script.status "unknown"`, `c32 script.output "` + textTooMany + `"`,
			`c33 script.status "unknown"`, `c33 script.output "` + textTooMany + `"`,
		})
	})
}

func TestPlainCollect(t *testing.T) {
	r := &fakeRunner{run: func(context.Context, []string) (runResult, error) {
		t.Error("the runner was called")
		return runResult{}, nil
	}}
	obs, _ := newPlugin(r, progOK()).Collect(context.Background(), map[string]any{"commands": []string{"x"}})
	equalLines(t, render(obs), []string{`x script.status "unknown"`, `x script.output "` + textNoCommands + `"`})
}

func TestNewIsUnavailableUntilTheRealRunner(t *testing.T) {
	p, ok := New().(checks.CommandPlugin)
	if !ok {
		t.Fatal("New() is not a CommandPlugin")
	}
	obs, _ := p.CollectWithCommands(context.Background(), map[string]any{"commands": []string{"x"}}, oneCommand("--password", "MARKER"))
	got := render(obs)
	equalLines(t, got, []string{`x script.status "unknown"`, `x script.output "` + prog + `: ` + errUnavailable.Error() + `"`})
}
