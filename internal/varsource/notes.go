package varsource

import (
	"fmt"
	"regexp"
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
)

func warning(code, file string, line int, format string, args ...any) Note {
	return Note{Level: LevelWarning, Code: code, File: file, Line: line, Message: fmt.Sprintf(format, args...)}
}

func hint(code, file string, line int, format string, args ...any) Note {
	return Note{Level: LevelHint, Code: code, File: file, Line: line, Message: fmt.Sprintf(format, args...)}
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
