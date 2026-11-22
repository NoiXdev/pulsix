// Check-mode claims (backlog 3.126, spec 2026-11-22 §1): what a run's claim
// ships for Ansible's check mode. The job row's check_mode is the authority
// for --check — set at enqueue from the sealed overrides, read by the claim
// statement's gate (only a worker that announced supports_check_mode gets
// such a row) — and the overrides add --diff, --tags and the unsaved draft
// to a check-mode row only.
package jobs

// claimCheck is the check-mode part of a run's claim.
type claimCheck struct {
	check, diff bool
	tags        []string
	// playbook is the draft that replaces the template's playbook; nil
	// runs the stored one.
	playbook *string
}

// checkClaim decides a run claim's check-mode fields from the row and its
// opened overrides:
//
//   - an ordinary row ships none of them: a draft, diff or tags in its
//     payload (which the service never writes) are ignored, so a draft
//     never runs for real; overrides that ask for check mode on a row that
//     is not one fail the claim — the requester wanted nothing changed;
//   - a check-mode row always ships check, even if its overrides lost the
//     flag, and the overrides' diff and tags; the draft only when the
//     overrides say check too, and only in place of an editor playbook.
//
// Tags are validated again: the worker passes them to ansible-runner, which
// splits its command line itself.
func checkClaim(job Job, tmpl TemplateRow, ov *RunOverrides) (claimCheck, error) {
	if !job.CheckMode {
		if ov.check() {
			return claimCheck{}, invalidDryRun("job %s: its overrides ask for check mode, its row does not", job.ID)
		}
		return claimCheck{}, nil
	}
	out := claimCheck{check: true}
	if ov == nil {
		return out, nil
	}
	if len(ov.Tags) > MaxCheckTags {
		return claimCheck{}, invalidDryRun("job %s: at most %d tags", job.ID, MaxCheckTags)
	}
	for _, tag := range ov.Tags {
		if !checkTagPattern.MatchString(tag) {
			return claimCheck{}, invalidDryRun("job %s: tag %q must be 1 to 64 letters, digits or _ . : -", job.ID, tag)
		}
	}
	out.diff, out.tags = ov.Diff, ov.Tags
	if ov.Playbook == nil || !ov.Check {
		return out, nil
	}
	if tmpl.PlaybookRepo != nil {
		return claimCheck{}, invalidDryRun("job %s: a draft can only replace an editor playbook", job.ID)
	}
	if *ov.Playbook == "" {
		return claimCheck{}, invalidDryRun("job %s: the draft playbook is empty", job.ID)
	}
	// The worker reads a "repo:" playbook as a repository reference, and
	// the claim would open that repository's credential for it.
	if _, _, isRepo := parsePlaybookRef(*ov.Playbook); isRepo {
		return claimCheck{}, invalidDryRun("job %s: the draft is not a playbook", job.ID)
	}
	out.playbook = ov.Playbook
	return out, nil
}
