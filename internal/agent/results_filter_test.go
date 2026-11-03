package agent

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// TestValidUTF8ResultsChecksEveryStringField: every string the result
// carries into its proto must be valid UTF-8, or gRPC refuses the chunk. A
// result whose check_key, item, metric or label is invalid is dropped; an
// error message is kept, since toCheckResultProtos sanitises it.
func TestValidUTF8ResultsChecksEveryStringField(t *testing.T) {
	at := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	label := func(s string) *string { return &s }
	ok := checks.Result{CheckKey: "disk", Item: "/", Metric: "disk.used_pct", Value: floatPtr(1), At: at}
	cases := map[string]struct {
		edit func(*checks.Result)
		keep bool
	}{
		"all valid":             {edit: func(*checks.Result) {}, keep: true},
		"invalid check_key":     {edit: func(r *checks.Result) { r.CheckKey = "di\xffsk" }},
		"invalid item":          {edit: func(r *checks.Result) { r.Item = "/q\xffz" }},
		"invalid metric":        {edit: func(r *checks.Result) { r.Metric = "disk.\xc3(" }},
		"valid label":           {edit: func(r *checks.Result) { r.Value, r.Label = nil, label("running") }, keep: true},
		"invalid label":         {edit: func(r *checks.Result) { r.Value, r.Label = nil, label("run\xffning") }},
		"invalid error is kept": {edit: func(r *checks.Result) { r.Value, r.Err = nil, "open /q\xffz: denied" }, keep: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			l := newLoopTest(t).newLoop()
			r := ok
			tc.edit(&r)

			got := l.validUTF8Results([]checks.Result{r})

			if kept := len(got) == 1; kept != tc.keep {
				t.Fatalf("result kept = %v, want %v", kept, tc.keep)
			}
		})
	}
}

// TestToCheckResultProtosSanitisesTheError: an error message that is not
// valid UTF-8 (it quotes a mangled file name, say) still goes out, with the
// invalid bytes replaced, so it stays useful and the chunk still encodes.
func TestToCheckResultProtosSanitisesTheError(t *testing.T) {
	at := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		err, want string
	}{
		"valid error unchanged":     {err: "open /etc: denied", want: "open /etc: denied"},
		"invalid bytes replaced":    {err: "open /q\xffz: denied", want: "open /q�z: denied"},
		"invalid sequence one rune": {err: "a\xc3(b", want: "a�(b"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pbs := toCheckResultProtos([]checks.Result{{CheckKey: "disk", Metric: "disk.used_pct", Err: tc.err, At: at}})
			if len(pbs) != 1 {
				t.Fatalf("got %d protos, want 1", len(pbs))
			}
			if got := pbs[0].GetError(); got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
		})
	}
}
