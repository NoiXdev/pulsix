package varsource

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
)

// Level says how much a note matters: a warning means something the source
// declares was skipped or changed; a hint explains a choice the parser made.
type Level string

// Levels.
const (
	LevelWarning Level = "warning"
	LevelHint    Level = "hint"
)

// Note is one finding of the parser, located by file (relative to the repo
// subpath) and line. Notes never stop a sync; the preview lists them. The
// user documentation lists every code with its cause.
type Note struct {
	Level   Level  `json:"level"`
	Code    string `json:"code"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// Note codes of the field mapping. The wording of a message is free; the
// code is the contract the docs reference names.
const (
	codeInvalidYAML          = "invalid_yaml"
	codeUnsupportedType      = "unsupported_type"
	codeChoicesConverted     = "choices_converted"
	codeSecretDefaultDropped = "secret_default_dropped"
	codeDefaultInvalid       = "default_invalid"
	codeDefaultDropped       = "default_dropped"
	codeNoLogCollection      = "no_log_collection"
	codeInvalidName          = "invalid_name"
	codeTooDeep              = "too_deep"
	codeTooManyFields        = "too_many_fields"
	codeTooManyOptions       = "too_many_options"
	codeEntryPointMissing    = "entry_point_missing"
	codeMarkerUnreadable     = "marker_unreadable"
	codeMarkerValueType      = "marker_value_type"
	codeMarkerEnumValue      = "marker_enum_value"
	codeTemplatedDefault     = "templated_default"
	codeFieldInvalid         = "field_invalid"
	codeDuplicateKey         = "duplicate_key"
	codeFileTooComplex       = "file_too_complex"
)

// Note codes of the playbook walk (playbook.go, roles.go, imports.go) and
// of the repo (discover.go, report.go).
const (
	codeDuplicateName          = "duplicate_name"
	codeMarkerMisplaced        = "marker_misplaced"
	codeRoleDynamic            = "role_dynamic"
	codeRoleCollection         = "role_collection"
	codeRoleNotFound           = "role_not_found"
	codeRoleWithoutSpecs       = "role_without_specs"
	codeRoleInvalid            = "role_invalid"
	codeImportCycle            = "import_cycle"
	codeImportTooDeep          = "import_too_deep"
	codeImportOutside          = "import_outside"
	codeImportNotFound         = "import_not_found"
	codeImportDynamic          = "import_dynamic"
	codeImportInvalid          = "import_invalid"
	codeNotAPlaybook           = "not_a_playbook"
	codeReadFailed             = "read_failed"
	codePathUnsupported        = "path_unsupported"
	codePathTooLong            = "path_too_long"
	codeFileTooLarge           = "file_too_large"
	codePlaybookTooComplex     = "playbook_too_complex"
	codeTooManySetNames        = "too_many_set_names"
	codeTooManyNotes           = "too_many_notes"
	codeTooManyPlaybooks       = "too_many_playbooks"
	codeReportTooLarge         = "report_too_large"
	codeTimeLimit              = "time_limit"
	codeReadCancelled          = "read_cancelled"
	codeInlineRolesUnresolved  = "inline_roles_unresolved"
	codeInlineImportUnresolved = "inline_import_unresolved"
)

// Note caps. One file raises at most maxNotesPerFile notes (a file of
// 512 KiB can otherwise raise tens of thousands, more than a report may
// hold); a playbook, its imports and roles together, and the repo's own
// list hold at most maxNotesPerList. Past a cap one too_many_notes warning
// counts the rest.
const (
	maxNotesPerFile = 200
	maxNotesPerList = 1000
)

// note records notes about y's file, up to maxNotesPerFile.
func (y *yamlFile) note(ns ...Note) {
	for _, n := range ns {
		if len(y.notes) < maxNotesPerFile {
			y.notes = append(y.notes, n)
		} else {
			y.dropped++
		}
	}
}

// takeNotes is the file's notes, with one too_many_notes warning when
// some were dropped.
func (y *yamlFile) takeNotes() []Note {
	if y.dropped == 0 {
		return y.notes
	}
	return append(slices.Clip(y.notes), tooManyNotes(y.file, y.dropped))
}

func tooManyNotes(file string, dropped int) Note {
	return warning(codeTooManyNotes, file, 0, "%d more notes are left out", dropped)
}

// capNotes drops repeated notes (a file read for two entry points raises
// its notes twice) and keeps the first limit; one too_many_notes warning on
// file counts the rest.
func capNotes(notes []Note, file string, limit int) []Note {
	seen := make(map[Note]bool, len(notes))
	out := make([]Note, 0, min(len(notes), limit+1))
	dropped := 0
	for _, n := range notes {
		if seen[n] {
			continue
		}
		seen[n] = true
		if len(out) == limit {
			dropped++
			continue
		}
		out = append(out, n)
	}
	if dropped > 0 {
		out = append(out, tooManyNotes(file, dropped))
	}
	return out
}

// short cuts a string taken from a file before it goes into a message, so
// building the message costs no more than the cut: a key or tag read once
// may be reached through aliases many times.
func short(s string) string { return clip(s, maxQuotedBytes) }

// ReadFailed is a read_failed warning about the repository as a whole,
// for a reader of the repo that could not run ParseRepo to its end.
func ReadFailed(message string) Note {
	return warning(codeReadFailed, "", 0, "%s", message)
}

// PathTooLong is a path_too_long warning for a reported playbook whose
// path is longer than limit bytes, for a reader of a report that cannot
// store it (the path is clipped in the note).
func PathTooLong(path string, limit int) Note {
	return warning(codePathTooLong, clip(path, maxRefBytes), 0, "a playbook path is at most %d bytes, this one has %d; not stored", limit, len(path))
}

// warning and hint build a note; the message is cut to maxMessageBytes.
// Every string argument taken from a file goes through short first.
func warning(code, file string, line int, format string, args ...any) Note {
	return Note{Level: LevelWarning, Code: code, File: file, Line: line, Message: clip(fmt.Sprintf(format, args...), maxMessageBytes)}
}

func hint(code, file string, line int, format string, args ...any) Note {
	return Note{Level: LevelHint, Code: code, File: file, Line: line, Message: clip(fmt.Sprintf(format, args...), maxMessageBytes)}
}

var yamlErrorLine = regexp.MustCompile(`line (\d+)`)

// invalidYAML is the one warning for a file that does not parse, at the
// line the YAML error names when it names one.
func invalidYAML(file string, err error) Note {
	line := 0
	if m := yamlErrorLine.FindStringSubmatch(err.Error()); m != nil {
		line, _ = strconv.Atoi(m[1])
	}
	return warning(codeInvalidYAML, file, line, "not valid YAML, nothing read from this file: %v", err)
}
