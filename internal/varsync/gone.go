package varsync

import (
	"fmt"
	"slices"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/varsource"
)

// Note codes of the preview, beside the parser's own. The user
// documentation lists each with its cause.
const (
	codeSourceUnavailable = "source_unavailable"
	codeNotChecked        = "not_checked"
	codeManualNameTaken   = "manual_name_taken"
	codeFieldsLeftOut     = "fields_left_out"
)

// unreadReason is why pb does not vouch for the absence of a name, or ""
// when it was read completely: the playbook read, no field cut by a limit,
// every file it uses read (a path counts as read when any of its uses was
// read or cut as a cycle, which only happens to a file already read), and
// every role it uses read. Only then may a field turn gone (spec 7.75,
// "gone means absent from a source that was read").
func unreadReason(pb varsource.Playbook) string {
	switch {
	case pb.Failed:
		return "the playbook was not read"
	case pb.FieldsCut:
		return "a limit left some of the playbook's variables out"
	}
	read := make(map[string]bool, len(pb.Files))
	for _, u := range pb.Files {
		if u.State == varsource.FileRead || u.State == varsource.FileCycle {
			read[u.Path] = true
		}
	}
	if len(read) == 0 {
		return "the playbook was not read"
	}
	for _, u := range pb.Files {
		if !read[u.Path] {
			return fmt.Sprintf("%q was not read (%s)", u.Path, u.State)
		}
	}
	for _, r := range pb.Roles {
		if r.State == varsource.RoleRead || r.State == varsource.RoleNoSpecs {
			continue
		}
		if r.Name == "" {
			return fmt.Sprintf("a role the playbook uses could not be read (%s)", r.State)
		}
		return fmt.Sprintf("role %q (entry point %s) was not read (%s)", r.Name, r.EntryPoint, r.State)
	}
	return ""
}

// absence decides which fields missing from a source are gone.
type absence struct {
	reason     string          // why nothing is gone; "" when the source was read completely
	unreadable map[string]bool // names the source declares but could not map
	kept       int
	keptWhy    string
}

func newAbsence(pb varsource.Playbook) *absence {
	a := &absence{reason: unreadReason(pb), unreadable: make(map[string]bool, len(pb.Unreadable))}
	for _, n := range pb.Unreadable {
		a.unreadable[n] = true
	}
	return a
}

// gone reports whether name, missing from the source's fields, is gone;
// a field kept is counted for the not_checked warning.
func (a *absence) gone(name string) bool {
	why := a.reason
	if why == "" && a.unreadable[name] {
		why = "the source declares them but they could not be read"
	}
	if why == "" {
		return true
	}
	a.kept, a.keptWhy = a.kept+1, why
	return false
}

// note is the not_checked warning about the fields kept, if any.
func (a *absence) note(file string) []varsource.Note {
	if a.kept == 0 {
		return nil
	}
	what := "1 field the source no longer lists is"
	if a.kept > 1 {
		what = fmt.Sprintf("%d fields the source no longer lists are", a.kept)
	}
	return []varsource.Note{{Level: varsource.LevelWarning, Code: codeNotChecked, File: file,
		Message: fmt.Sprintf("%s kept unchanged, because %s", what, a.keptWhy)}}
}

// markGone is f as stored once the source no longer declares it: origin
// gone, everything else (its source, an adjusted field's source version,
// active) kept.
func markGone(f schema.Field) schema.Field {
	f.Origin = schema.OriginGone
	return f
}

// linked reports whether f came from the source and follows it.
func linked(f schema.Field) bool {
	return slices.Contains([]schema.Origin{schema.OriginSource, schema.OriginAdjusted, schema.OriginGone}, f.EffectiveOrigin())
}
