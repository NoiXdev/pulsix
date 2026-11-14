package agents

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// Bounds on what one heartbeat may store about the checks defined in the
// agent's agent.yaml (backlog 3.5a), beyond the ones both sides share
// (checkplugins.MaxLocalChecks, MaxLocalCheckParamsBytes and
// MaxLocalCheckProblems). The agent keeps to the shared ones itself
// (internal/agent/secrets); the manager enforces them again because the
// stream is the agent's to fill — an untrusted or broken agent must not be
// able to store more, or other, than the checks the agent could run and a
// short list of one-line problems.
const (
	// MaxLocalCheckProblemBytes bounds one problem, cut on a rune boundary.
	MaxLocalCheckProblemBytes = 256
	// maxLoggedKeyBytes bounds a dropped check's key in the log line.
	maxLoggedKeyBytes = 64
	// maxLoggedDrops bounds the log lines one heartbeat writes about
	// dropped checks; the rest are counted in one more line.
	maxLoggedDrops = checkplugins.MaxLocalChecks
	// maxExaminedLocalChecks bounds how many entries of one heartbeat are
	// looked at, in the order sent: twice what can be kept, so an agent
	// that keeps to the cap is always judged whole, and a longer list costs
	// no sorting, parsing or allocation beyond it. The entries after it are
	// dropped unexamined and only counted.
	maxExaminedLocalChecks = 2 * checkplugins.MaxLocalChecks
)

// Fixed reasons for dropping a local check over a shared bound.
var (
	tooManyLocalChecks = "more than " + strconv.Itoa(checkplugins.MaxLocalChecks) + " local checks"
	localParamsTooLong = "the params are over " + strconv.Itoa(checkplugins.MaxLocalCheckParamsBytes) + " bytes"
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

// localCheckDrops is what one heartbeat's validation refused: the examined
// entries it dropped, each with its reason, and how many entries were not
// examined at all (beyond maxExaminedLocalChecks).
type localCheckDrops struct {
	examined   []droppedLocalCheck
	unexamined int
}

// localChecksReportOf validates one heartbeat's local checks and bounds its
// problems, the way secretsReportOf bounds the secrets report; it returns
// the report and the checks it dropped.
//
// Only the first maxExaminedLocalChecks entries as sent are looked at; the
// rest are dropped unexamined.
//
// An empty secrets state means an agent older than agent.yaml, so the
// report is unknown whatever rides along. Otherwise the checks are taken in
// key order (a stable sort, so of two with the same key the one sent first
// comes first) and each is kept only when its key is LocalCheckPrefix plus
// CheckKeyPattern and not seen before, its plugin is known and run by the
// agent, its interval is within MinInterval and MaxInterval, its params
// JSON is at most checkplugins.MaxLocalCheckParamsBytes and an object
// without a NUL that checkplugins.Validate accepts, and fewer than
// checkplugins.MaxLocalChecks were kept before it (checked right after the
// key, so nothing past the cap is parsed). Problems are kept as sent, the
// first checkplugins.MaxLocalCheckProblems, each bounded by boundLine to
// MaxLocalCheckProblemBytes (which also replaces a NUL, as PostgreSQL text
// refuses one).
func localChecksReportOf(secretsState string, in []LocalCheckInput, problems []string) (LocalChecksReport, localCheckDrops) {
	if secretsState == "" {
		return LocalChecksReport{}, localCheckDrops{}
	}
	var drops localCheckDrops
	if len(in) > maxExaminedLocalChecks {
		in, drops.unexamined = in[:maxExaminedLocalChecks], len(in)-maxExaminedLocalChecks
	}
	sorted := slices.Clone(in)
	slices.SortStableFunc(sorted, func(a, b LocalCheckInput) int { return cmp.Compare(a.Key, b.Key) })
	kept := make([]LocalCheck, 0, min(len(sorted), checkplugins.MaxLocalChecks))
	seen := make(map[string]bool, len(sorted))
	for _, c := range sorted {
		check, reason := validLocalCheck(c, seen, len(kept))
		if reason != "" {
			drops.examined = append(drops.examined, droppedLocalCheck{key: loggableKey(c.Key), reason: reason})
			continue
		}
		kept = append(kept, check)
	}
	bounded := make([]string, 0, min(len(problems), checkplugins.MaxLocalCheckProblems))
	for _, p := range problems[:min(len(problems), checkplugins.MaxLocalCheckProblems)] {
		bounded = append(bounded, boundLine(p, MaxLocalCheckProblemBytes))
	}
	return LocalChecksReport{Known: true, Checks: kept, Problems: bounded}, drops
}

// validLocalCheck is one check's verdict: the check, or a fixed reason it
// is dropped. seen collects every well-formed key met so far, so of two
// checks with the same key the second is dropped whatever became of the
// first; kept is how many checks were kept before this one — once that is
// checkplugins.MaxLocalChecks, a check is dropped before its plugin or
// params are looked at.
func validLocalCheck(c LocalCheckInput, seen map[string]bool, kept int) (LocalCheck, string) {
	if !checkplugins.ValidLocalCheckKey(c.Key) {
		return LocalCheck{}, "the key is not local- followed by a valid check key"
	}
	if seen[c.Key] {
		return LocalCheck{}, "a duplicate key"
	}
	seen[c.Key] = true
	if kept >= checkplugins.MaxLocalChecks {
		return LocalCheck{}, tooManyLocalChecks
	}
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
	if len(c.ParamsJSON) > checkplugins.MaxLocalCheckParamsBytes {
		return LocalCheck{}, localParamsTooLong
	}
	var decoded any
	if err := json.Unmarshal([]byte(c.ParamsJSON), &decoded); err != nil {
		return LocalCheck{}, "the params are not a JSON object"
	}
	params, ok := decoded.(map[string]any)
	if !ok {
		return LocalCheck{}, "the params are not a JSON object"
	}
	if holdsNUL(params) {
		return LocalCheck{}, "the params contain a NUL character"
	}
	if err := checkplugins.Validate(c.Plugin, params); err != nil {
		return LocalCheck{}, "the plugin refuses the params"
	}
	if canonical, err := checkplugins.CanonicalParams(params); err != nil || len(canonical) > checkplugins.MaxLocalCheckParamsBytes {
		return LocalCheck{}, localParamsTooLong
	}
	return LocalCheck{Key: c.Key, Plugin: c.Plugin, Params: params, Interval: interval}, ""
}

// holdsNUL reports whether a JSON-decoded value holds a NUL (U+0000) in any
// string — an object's names included. PostgreSQL's jsonb refuses one
// (\u0000), so a check carrying it could not be stored, and with it the
// whole heartbeat.
func holdsNUL(v any) bool {
	switch v := v.(type) {
	case string:
		return strings.IndexByte(v, 0) >= 0
	case []any:
		return slices.ContainsFunc(v, holdsNUL)
	case map[string]any:
		for name, value := range v {
			if strings.IndexByte(name, 0) >= 0 || holdsNUL(value) {
				return true
			}
		}
	}
	return false
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
// the rest, the unexamined entries included.
func logDroppedLocalChecks(ctx context.Context, log *slog.Logger, serial string, drops localCheckDrops) {
	listed := drops.examined[:min(len(drops.examined), maxLoggedDrops)]
	for _, d := range listed {
		log.WarnContext(ctx, "dropped a local check the agent reported", "serial", serial, "key", d.key, "reason", d.reason)
	}
	if more := len(drops.examined) - len(listed) + drops.unexamined; more > 0 {
		log.WarnContext(ctx, "dropped more local checks the agent reported", "serial", serial, "count", more)
	}
}
