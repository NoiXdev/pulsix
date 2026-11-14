// Package script is the agent's custom script check plugin (backlog
// 3.6g): it runs commands the host defines in agent.yaml, which the
// manager's check only names, and reports each command's verdict, message
// and values. The Plugin contract and the shared observation helpers are
// in internal/agent/checks; the plugin is a checks.CommandPlugin, so the
// scheduler hands it the lookup for the file's commands.
//
// Per command, item <command>: script.status (ok, warn, crit, unknown),
// script.output (the message, cleaned into a reportable label),
// script.exit_code when the process ran to an exit, and
// script.duration_seconds when a process ran at all; item
// <command>:<label>: script.value, one per perfdata or JSON metric value
// (at most 64). Output is read in the Nagios format (exit code and
// perfdata, nagios.go) or the JSON format (jsonformat.go).
//
// Before every run the program, and every argument that is an absolute
// path naming a regular file, passes the checker; the runner then runs the
// argv directly within timeout_seconds (runner.go). At most maxParallel
// commands of one check run at once. Every failure (an undefined command,
// a refused file, a timeout, a start failure, an earlier run still stuck,
// even a panic) is reported as status unknown with a fixed output that
// never quotes an argument other than the program or a checked path: an
// argument may hold a credential, and the scheduler drops a command
// plugin's advisory error, so the observations carry every failure.
package script

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

const (
	// maxParallel bounds the commands of one check that run at once.
	maxParallel = 4
	// maxCommands bounds the commands one check runs (the manager's own
	// bound on the commands param); the rest are reported unknown.
	maxCommands = 32
)

// The fixed outputs of a command that produced no usable output.
const (
	textBusy        = "an earlier run of this command has not exited yet"
	textStartFailed = "could not start the program"
	textNoExit      = "the program ended without an exit code"
	textInternal    = "internal error while running the command"
	// textUncheckedProgram follows the program's path; an argument that
	// could not be checked is not named at all.
	textUncheckedProgram  = "could not be checked"
	textUncheckedArgument = "an argument could not be checked"
	textNoCommands        = "commands are not available"
	textTooMany           = "the check lists more than 32 commands"
	textBadFormat         = `the check's parameter "format" is invalid`
	textBadTimeout        = `the check's parameter "timeout_seconds" is invalid`
)

const (
	metricStatus   = "script.status"
	metricOutput   = "script.output"
	metricExitCode = "script.exit_code"
	metricDuration = "script.duration_seconds"
	metricValue    = "script.value"
)

// scriptPlugin runs host-defined commands. It holds no state of its own:
// the runner keeps what must outlive a round (the commands it could not
// stop), so one value serves every check key at once.
type scriptPlugin struct {
	run   runner
	check checker
}

// New returns the script plugin. Until the real runner and checker exist
// it refuses every program as not available on this system.
func New() checks.Plugin {
	return newPlugin(unavailable{}, unavailable{})
}

func newPlugin(r runner, c checker) *scriptPlugin {
	return &scriptPlugin{run: r, check: c}
}

func (*scriptPlugin) Name() string { return "script" }

// Collect is only reached when the plugin is called without the
// scheduler's lookup: every listed command is reported unknown.
func (*scriptPlugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	names, err := commandNames(params)
	if err != nil {
		return nil, err
	}
	var obs []checks.Observation
	for _, name := range names {
		obs = append(obs, noRun(name, textNoCommands)...)
	}
	return obs, nil
}

// CollectWithCommands runs every command the check lists and reports each
// one, in the order listed. Only a dead ctx (the agent stopping) fails
// outright, with no observations, as the Plugin contract asks.
func (p *scriptPlugin) CollectWithCommands(ctx context.Context, params map[string]any, commands checks.Commands) (obs []checks.Observation, err error) {
	var names []string
	defer func() {
		if recover() != nil {
			obs, err = nil, errors.New("script: internal error")
			for _, name := range names {
				obs = append(obs, noRun(name, textInternal)...)
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	names, err = commandNames(params)
	if err != nil {
		return nil, err
	}
	opts, problem := parseOptions(params)
	if problem != "" {
		for _, name := range names {
			obs = append(obs, noRun(name, problem)...)
		}
		return obs, nil
	}
	run := names
	if len(run) > maxCommands {
		run = run[:maxCommands]
	}
	results := p.runAll(ctx, run, commands, opts)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, r := range results {
		obs = append(obs, r...)
	}
	for _, name := range names[len(run):] {
		obs = append(obs, noRun(name, textTooMany)...)
	}
	return obs, nil
}

// commandNames reads the commands param: names in order, each once,
// leaving out an empty one and one that is no reportable item (the manager
// allows neither).
func commandNames(params map[string]any) ([]string, error) {
	list, _, err := checks.ParamStrings(params, "commands")
	if err != nil {
		return nil, fmt.Errorf("script: %w", err)
	}
	seen := map[string]bool{}
	var names []string
	for _, name := range list {
		if name == "" || seen[name] || !checks.ReportableItem(name) {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

// runAll runs names with at most maxParallel at once and returns each
// one's observations, in order. It stops starting commands once ctx ends.
func (p *scriptPlugin) runAll(ctx context.Context, names []string, commands checks.Commands, opts options) [][]checks.Observation {
	results := make([][]checks.Observation, len(names))
	sem := make(chan struct{}, maxParallel)
	done := make(chan struct{}, len(names))
	started := 0
start:
	for i, name := range names {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break start
		}
		started++
		go func() {
			defer func() { <-sem; done <- struct{}{} }()
			results[i] = p.runOne(ctx, name, commands, opts)
		}()
	}
	for range started {
		<-done
	}
	return results
}

// runOne looks name up, checks its files, runs it and reports the result.
// A panic anywhere in there is reported as textInternal: its value may
// quote an argument, so it is dropped.
func (p *scriptPlugin) runOne(ctx context.Context, name string, commands checks.Commands, opts options) (obs []checks.Observation) {
	defer func() {
		if recover() != nil {
			obs = noRun(name, textInternal)
		}
	}()
	argv, ok, problem := commands.Command(name)
	if !ok || len(argv) == 0 {
		return noRun(name, notDefined(name, problem))
	}
	if refusal := p.checkFiles(argv); refusal != "" {
		return noRun(name, refusal)
	}

	rctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	res, err := p.run.Run(rctx, argv)
	switch {
	case errors.Is(err, errTimeout) || (err != nil && ctx.Err() == nil && errors.Is(rctx.Err(), context.DeadlineExceeded)):
		d := res.duration
		if d <= 0 {
			d = opts.timeout
		}
		return append(noRun(name, fmt.Sprintf("timed out after %ds", int64(opts.timeout/time.Second))),
			checks.ValueObservation(name, metricDuration, d.Seconds()))
	case errors.Is(err, errBusy):
		return noRun(name, textBusy)
	case err != nil:
		return noRun(name, startFailure(err))
	}
	return report(name, res, opts.format)
}

// notDefined is the output for a command the file does not define, with
// the reason the file has none when there is one.
func notDefined(name, problem string) string {
	msg := `command "` + name + `" is not defined on this host`
	if problem != "" {
		msg += " (" + problem + ")"
	}
	return msg
}

// checkFiles checks the program, then every argument that is an absolute
// path naming a regular file, and returns why the first one that does not
// pass fails, or "" when every one passes. It fails closed: a file the
// checker could not check stops the run like a refused one. A path is
// echoed only when it is the program or a file the checker inspected and
// refused (checker), and only the refusal's own reason goes with it.
func (p *scriptPlugin) checkFiles(argv []string) string {
	prog := argv[0]
	if !filepath.IsAbs(prog) {
		return prog + ": not an absolute path"
	}
	if err := p.check.Check(prog); err != nil {
		if reason, ok := refusalReason(err); ok {
			return prog + ": " + reason
		}
		if errors.Is(err, errNotFile) {
			return prog + ": " + errNotFile.Error()
		}
		return prog + ": " + textUncheckedProgram
	}
	for _, arg := range argv[1:] {
		if !filepath.IsAbs(arg) {
			continue
		}
		err := p.check.Check(arg)
		if reason, ok := refusalReason(err); ok {
			return arg + ": " + reason
		}
		if err != nil && !errors.Is(err, errNotFile) {
			return textUncheckedArgument
		}
	}
	return ""
}

// refusalReason is the reason of a refuse error in err's chain, without
// any text a wrapper added around it.
func refusalReason(err error) (string, bool) {
	var r refusal
	if !errors.As(err, &r) {
		return "", false
	}
	return string(r), true
}

// startFailure words a failure to start the program: the fixed text, plus
// the operating system's error number when there is one — its text cannot
// hold an argument, the rest of err can.
func startFailure(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return textStartFailed + ": " + errno.Error()
	}
	return textStartFailed
}

// report turns a finished run into the command's observations.
func report(name string, res runResult, f format) []checks.Observation {
	res.stdout = bytes.TrimPrefix(res.stdout, byteOrderMark)
	var st status
	var message string
	var values []itemValue
	switch {
	case !res.exited:
		st, message = statusUnknown, textNoExit
	case f == formatJSON:
		st, message, values = parseJSON(name, res)
	default:
		st = nagiosStatus(res)
		message, values = parseNagios(name, res.stdout)
	}
	obs := []checks.Observation{
		checks.LabelObservation(name, metricStatus, string(st)),
		checks.LabelObservation(name, metricOutput, cleanMessage(message)),
	}
	if res.exited {
		obs = append(obs, checks.ValueObservation(name, metricExitCode, float64(res.exitCode)))
	}
	obs = append(obs, checks.ValueObservation(name, metricDuration, res.duration.Seconds()))
	for _, v := range values {
		obs = append(obs, checks.ValueObservation(v.item, metricValue, v.value))
	}
	return obs
}

// byteOrderMark is UTF-8's, which Windows PowerShell 5.1 writes ahead of
// its output; it is dropped before either format reads the output.
var byteOrderMark = []byte("\xEF\xBB\xBF")

// noRun is a command's report when no output came: status unknown and
// text, cleaned, as output.
func noRun(name, text string) []checks.Observation {
	return []checks.Observation{
		checks.LabelObservation(name, metricStatus, string(statusUnknown)),
		checks.LabelObservation(name, metricOutput, cleanMessage(text)),
	}
}
