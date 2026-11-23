// Check-mode runs: a playbook dry run is a manual template run whose
// sealed overrides ask Ansible for --check (spec 2026-11-22 §1). This file
// holds the rules such a request must meet before anything is enqueued.
package jobs

import (
	"errors"
	"fmt"
	"regexp"
)

var (
	// ErrInvalidDryRun means a check-mode request breaks one of its own
	// rules (tags, a draft, the template's playbook, the trigger); the
	// error text names which.
	ErrInvalidDryRun = errors.New("invalid dry run")
	// ErrDryRunNotRetryable means a retry of a dry run's failed hosts was
	// asked for: the retry would run them for real.
	ErrDryRunNotRetryable = errors.New("a dry run cannot be retried: start a new dry run, or run the template")
)

// DryRun is what a check-mode run records about itself on its job row
// (jobs.dry_run, migration 00116), so the run view can show it without
// opening the sealed payload: the tags, whether --diff was asked for and
// whether an unsaved draft ran. Never the draft's text or its hash.
type DryRun struct {
	Tags  []string `json:"tags"`
	Diff  bool     `json:"diff"`
	Draft bool     `json:"draft"`
}

// dryRun is the record a run with these overrides carries: nil unless it
// is a check run. Tags is never nil, so the record reads "no tags" as [].
func (o *RunOverrides) dryRun() *DryRun {
	if !o.check() {
		return nil
	}
	tags := append([]string{}, o.Tags...)
	return &DryRun{Tags: tags, Diff: o.Diff, Draft: o.Playbook != nil}
}

// MaxDraftBytes caps a dry run's draft playbook: the size an editor playbook
// may be stored with (playbooks.MaxInlineBytes, which a test keeps equal).
const MaxDraftBytes = 512 * 1024

// MaxCheckTags caps the tags of one check-mode run.
const MaxCheckTags = 32

// checkTagPattern is one --tags entry: no comma (Ansible's list
// separator), no space, at most 64 characters.
var checkTagPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

func invalidDryRun(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidDryRun}, args...)...)
}

// checkRunRequest validates a request's check-mode overrides on their own:
// a draft, tags and diff only with Check (a real run would drop the tags
// and so run more than was asked for), valid tags, and a check run only as
// a manual run without follow-ups. Schedules, webhooks, chains and the system never
// start one (spec non-goals).
func checkRunRequest(req RunRequest) error {
	ov := req.Overrides
	if ov == nil {
		return nil
	}
	if ov.Playbook != nil {
		if !ov.Check {
			return invalidDryRun("a draft playbook only runs in check mode")
		}
		if *ov.Playbook == "" {
			return invalidDryRun("the draft playbook is empty")
		}
		if len(*ov.Playbook) > MaxDraftBytes {
			return invalidDryRun("the draft playbook exceeds %d KiB", MaxDraftBytes/1024)
		}
	}
	if !ov.Check && (len(ov.Tags) > 0 || ov.Diff) {
		return invalidDryRun("tags and diff only apply to a dry run")
	}
	if len(ov.Tags) > MaxCheckTags {
		return invalidDryRun("at most %d tags", MaxCheckTags)
	}
	for _, tag := range ov.Tags {
		if !checkTagPattern.MatchString(tag) {
			return invalidDryRun("tag %q must be 1 to 64 letters, digits or _ . : -", tag)
		}
	}
	if !ov.Check {
		return nil
	}
	if req.Trigger != TriggerManual {
		return invalidDryRun("only a manual run can be a dry run")
	}
	if req.FollowUps != nil {
		return invalidDryRun("a dry run has no follow-ups")
	}
	return nil
}

// checkRunTemplate validates a request against its template: the playbook
// a dry run was started from must be the template's, and a draft replaces
// an editor playbook only (a repository playbook's text is not Pulsix's).
func checkRunTemplate(tmpl TemplateRow, req RunRequest) error {
	if req.PlaybookID != nil && tmpl.PlaybookID != *req.PlaybookID {
		return invalidDryRun("the template does not run this playbook")
	}
	if req.Overrides != nil && req.Overrides.Playbook != nil && tmpl.PlaybookRepo != nil {
		return invalidDryRun("a draft can only replace an editor playbook")
	}
	return nil
}
