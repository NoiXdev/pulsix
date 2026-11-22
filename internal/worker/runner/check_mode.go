// Check mode (backlog 3.126): a playbook dry run's claim carries check,
// diff and tags, which reach ansible-playbook as --check, --diff and
// --tags. ansible-runner 2.4.3's `run` subcommand has no option of its own
// for any of them; its --cmdline option ("command line options to pass to
// ansible-playbook at execution time") carries them. ansible-runner splits
// that string with Python's shlex.split and places the tokens right after
// the ansible-playbook binary (RunnerConfig.generate_ansible_command; the
// env/cmdline file is the same string's fallback, read only when the
// option is empty). No shell is involved: the worker passes the option as
// one argv element, and every token it builds is single-quoted where it
// carries a value, so shlex hands each back whole.
package runner

import (
	"fmt"
	"strings"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// cmdlineArg is the ansible-runner argv element for a check-mode job, or
// "" when the job asks for none of check, diff and tags. The option and its
// value form ONE element ("--cmdline=..."): argparse takes a separate value
// that starts with "-" — "--check" alone — for an option of its own.
func cmdlineArg(job *workerpb.Job) string {
	line := checkCmdline(job)
	if line == "" {
		return ""
	}
	return "--cmdline=" + line
}

// checkCmdline is the --cmdline value: --check, --diff and --tags in that
// order, each only when the job asks for it.
func checkCmdline(job *workerpb.Job) string {
	var parts []string
	if job.GetCheck() {
		parts = append(parts, "--check")
	}
	if job.GetDiff() {
		parts = append(parts, "--diff")
	}
	if tags := job.GetTags(); len(tags) > 0 {
		parts = append(parts, "--tags", shlexQuote(strings.Join(tags, ",")))
	}
	return strings.Join(parts, " ")
}

// shlexQuote is Python's shlex.quote for a value that is always quoted:
// the value in single quotes, each ' inside written as '"'"'. shlex.split
// (POSIX mode) reads it back as exactly the value.
func shlexQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'"'"'`) + "'"
}

// checkKind refuses check mode on any job but a template run: the system
// playbooks and ad-hoc commands have no dry run, and running one for real
// when the claim asked for a dry run would be the one failure check mode
// must never have.
func checkKind(job *workerpb.Job) error {
	if (job.GetCheck() || job.GetDiff() || len(job.GetTags()) > 0) && job.GetKind() != kindRun {
		return fmt.Errorf("a %s job cannot run in check mode", job.GetKind())
	}
	return nil
}
