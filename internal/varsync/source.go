package varsync

import "github.com/noixdev/pulsix/internal/varsource"

// Source is a playbook's variable sources as a preview reads them.
// Available is false when nothing in it may change a template: no record,
// the playbook not found in it, left out by the last sync's caps, not read
// (Failed), or synced by a worker that reads no sources. Reason says which.
type Source struct {
	Rev       string
	Available bool
	Playbook  varsource.Playbook
	Reason    string
}

// Read is how a playbook's variable sources were last read: the parts of
// playbooks.VarSource that decide whether a preview may use them.
type Read struct {
	Rev         string
	Found       bool
	LeftOut     bool
	WorkerReads bool
	Playbook    varsource.Playbook
}

// NewSource is r as a preview's source, available only when its playbook
// was read by the last sync of a worker that reads sources.
func NewSource(r Read) Source {
	s := Source{Rev: r.Rev, Playbook: r.Playbook}
	switch {
	case !r.Found:
		s.Reason = "no variable sources were read for this playbook yet; sync its repository"
	case !r.WorkerReads:
		s.Reason = "the worker that synced this repository does not read variable sources; nothing changes until a newer worker syncs it"
	case r.LeftOut:
		s.Reason = "the last sync left this playbook out (a limit was reached); nothing changes until a sync reads it"
	case r.Playbook.Failed:
		s.Reason = "the playbook could not be read; nothing changes until it can (see the warnings)"
	default:
		s.Available = true
	}
	return s
}

// Newer reports whether src is a read the template has not applied yet:
// an available source whose revision is not applied. A source without a
// revision (no record, a playbook left out) is never newer.
func Newer(applied string, src Source) bool {
	return src.Available && src.Rev != "" && src.Rev != applied
}

// AppliedRev is the source revision a template records after applying p
// over a template that recorded previous: p's revision, or previous when
// p's source was not available or names no revision, so an unusable read
// never becomes the applied one.
func (p Preview) AppliedRev(previous string) string {
	if !p.available || p.SourceRev == "" {
		return previous
	}
	return p.SourceRev
}
