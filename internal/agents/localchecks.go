package agents

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// Bounds on what one heartbeat may store about the checks defined in the
// agent's agent.yaml (backlog 3.5a). The agent keeps to them itself
// (internal/agent/secrets); the manager enforces them again because the
// stream is the agent's to fill — an untrusted or broken agent must not be
// able to store more, or other, than the checks the agent could run and a
// short list of one-line problems.
const (
	// MaxLocalChecks is how many local checks are kept: the agent runs at
	// most as many.
	MaxLocalChecks = 32
	// MaxLocalCheckParamsBytes bounds one check's params JSON, as sent and
	// as stored.
	MaxLocalCheckParamsBytes = 2048
	// MaxLocalCheckProblems is how many problems are kept, in the order
	// sent.
	MaxLocalCheckProblems = 32
	// MaxLocalCheckProblemBytes bounds one problem, cut on a rune boundary.
	MaxLocalCheckProblemBytes = 256
	// maxLoggedKeyBytes bounds a dropped check's key in the log line.
	maxLoggedKeyBytes = 64
	// maxLoggedDrops bounds the log lines one heartbeat writes about
	// dropped checks; the rest are counted in one more line.
	maxLoggedDrops = MaxLocalChecks
)

// LocalCheckInput is one local check as the heartbeat carries it: the
// agent's own canonical rendering, not yet validated.
type LocalCheckInput struct {
	Key, Plugin, ParamsJSON string
	IntervalSeconds         int32
}

// LocalCheck is one validated local check: a key starting with
// checkplugins.LocalCheckPrefix, an agent-run plugin, params that plugin
// accepts (JSON-decoded: numbers are float64) and an interval within
// checkplugins' bounds.
type LocalCheck struct {
	Key, Plugin string
	Params      map[string]any
	Interval    time.Duration
}

// LocalChecksReport is what an agent last said about the checks defined in
// its agent.yaml: the ones it runs and the problems that kept others from
// running.
//
// Known is false when there is no report: the agent is older than local
// checks (it sends no secrets state, see secretsReportOf), or the
// registration has not sent a heartbeat yet. Checks and Problems are then
// nil; when Known, both are non-nil (possibly empty), Checks in key order.
type LocalChecksReport struct {
	Known    bool
	Checks   []LocalCheck
	Problems []string
}

// droppedLocalCheck is one check the manager refused: its key as it may
// be logged and a fixed reason — never the params.
type droppedLocalCheck struct {
	key, reason string
}

// localChecksReportOf validates one heartbeat's local checks and bounds its
// problems, the way secretsReportOf bounds the secrets report; it returns
// the report and the checks it dropped.
//
// An empty secrets state means an agent older than agent.yaml, so the
// report is unknown whatever rides along. Otherwise the checks are taken in
// key order (a stable sort, so of two with the same key the one sent first
// comes first) and each is kept only when its key is LocalCheckPrefix plus
// CheckKeyPattern and not seen before, its plugin is known and run by the
// agent, its interval is within MinInterval and MaxInterval, its params
// JSON is at most MaxLocalCheckParamsBytes and an object that
// checkplugins.Validate accepts, and fewer than MaxLocalChecks were kept
// before it. Problems are kept as sent, the first MaxLocalCheckProblems,
// each bounded by boundLine to MaxLocalCheckProblemBytes.
func localChecksReportOf(secretsState string, in []LocalCheckInput, problems []string) (LocalChecksReport, []droppedLocalCheck) {
	if secretsState == "" {
		return LocalChecksReport{}, nil
	}
	sorted := slices.Clone(in)
	slices.SortStableFunc(sorted, func(a, b LocalCheckInput) int { return cmp.Compare(a.Key, b.Key) })
	kept := make([]LocalCheck, 0, min(len(sorted), MaxLocalChecks))
	var dropped []droppedLocalCheck
	seen := make(map[string]bool, len(sorted))
	for _, c := range sorted {
		check, reason := validLocalCheck(c, seen, len(kept))
		if reason != "" {
			dropped = append(dropped, droppedLocalCheck{key: loggableKey(c.Key), reason: reason})
			continue
		}
		kept = append(kept, check)
	}
	bounded := make([]string, 0, min(len(problems), MaxLocalCheckProblems))
	for _, p := range problems[:min(len(problems), MaxLocalCheckProblems)] {
		bounded = append(bounded, boundLine(p, MaxLocalCheckProblemBytes))
	}
	return LocalChecksReport{Known: true, Checks: kept, Problems: bounded}, dropped
}

// validLocalCheck is one check's verdict: the check, or a fixed reason it
// is dropped. seen collects every well-formed key met so far, so of two
// checks with the same key the second is dropped whatever became of the
// first; kept is how many checks were kept before this one.
func validLocalCheck(c LocalCheckInput, seen map[string]bool, kept int) (LocalCheck, string) {
	if !checkplugins.ValidLocalCheckKey(c.Key) {
		return LocalCheck{}, "the key is not local- followed by a valid check key"
	}
	if seen[c.Key] {
		return LocalCheck{}, "a duplicate key"
	}
	seen[c.Key] = true
	d, ok := checkplugins.Get(c.Plugin)
	if !ok {
		return LocalCheck{}, "an unknown plugin"
	}
	if d.Runs != checkplugins.Agent {
		return LocalCheck{}, "a plugin that runs on the server"
	}
	interval := time.Duration(c.IntervalSeconds) * time.Second
	if interval < checkplugins.MinInterval || interval > checkplugins.MaxInterval {
		return LocalCheck{}, "the interval is out of bounds"
	}
	if len(c.ParamsJSON) > MaxLocalCheckParamsBytes {
		return LocalCheck{}, "the params are over 2048 bytes"
	}
	var decoded any
	if err := json.Unmarshal([]byte(c.ParamsJSON), &decoded); err != nil {
		return LocalCheck{}, "the params are not a JSON object"
	}
	params, ok := decoded.(map[string]any)
	if !ok {
		return LocalCheck{}, "the params are not a JSON object"
	}
	if err := checkplugins.Validate(c.Plugin, params); err != nil {
		return LocalCheck{}, "the plugin refuses the params"
	}
	if canonical, err := checkplugins.CanonicalParams(params); err != nil || len(canonical) > MaxLocalCheckParamsBytes {
		return LocalCheck{}, "the params are over 2048 bytes"
	}
	if kept >= MaxLocalChecks {
		return LocalCheck{}, "more than 32 local checks"
	}
	return LocalCheck{Key: c.Key, Plugin: c.Plugin, Params: params, Interval: interval}, ""
}

// loggableKey is a dropped check's key for a log line: boundLine's rule,
// cut to maxLoggedKeyBytes, and any rune that would not print (a format
// character such as a direction override) as '?'.
func loggableKey(key string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, boundLine(key, maxLoggedKeyBytes))
}

// logDroppedLocalChecks writes one warning per dropped check — key and
// reason, never params — up to maxLoggedDrops, and one more line counting
// the rest.
func logDroppedLocalChecks(ctx context.Context, log *slog.Logger, serial string, dropped []droppedLocalCheck) {
	for i, d := range dropped {
		if i == maxLoggedDrops {
			log.WarnContext(ctx, "dropped more local checks the agent reported", "serial", serial, "count", len(dropped)-i)
			return
		}
		log.WarnContext(ctx, "dropped a local check the agent reported", "serial", serial, "key", d.key, "reason", d.reason)
	}
}
