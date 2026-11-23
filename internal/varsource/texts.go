package varsource

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// The playbook texts a repo sync ships beside its report (backlog 3.127):
// a manager that asks for them (the claim's want_playbook_texts) gets each
// reported playbook's file, path → text, under PlaybookTextsStatsKey, and
// every path it does not get under PlaybookTextsLeftOutStatsKey, path →
// one of the Text* reasons. Both the worker that sends them and the
// manager that stores them read the names here; the values are a wire
// contract between versions and never change.
const (
	// PlaybookTextsStatsKey is the sync result's stats key of the texts
	// (an object of path → text).
	PlaybookTextsStatsKey = "playbook_texts"
	// PlaybookTextsLeftOutStatsKey is the sync result's stats key of the
	// paths sent without a text (an object of path → reason).
	PlaybookTextsLeftOutStatsKey = "playbook_texts_left_out"

	// MaxPlaybookTextsBytes bounds all texts of one sync together, as the
	// result carries them (JSON-escaped), so that the result stays under
	// what a worker may send beside the MaxReportBytes report. One text is
	// bounded by MaxFileBytes.
	MaxPlaybookTextsBytes = 4 << 20
)

// Why a reported playbook's text was not sent.
const (
	// TextTooLarge: the file is larger than MaxFileBytes.
	TextTooLarge = "too_large"
	// TextBudget: the file would take the texts past MaxPlaybookTextsBytes.
	TextBudget = "budget"
	// TextNotUTF8: the file is not valid UTF-8, or holds a NUL byte (no
	// text column can store one).
	TextNotUTF8 = "not_utf8"
	// TextUnreadable: the file could not be read, or lies outside the
	// files the sync may read (a link out of the subpath, git's own files).
	TextUnreadable = "unreadable"
)

// TextProblem is why text cannot be sent or stored as a playbook text, or
// "" when it can: longer than MaxFileBytes is TextTooLarge; not valid
// UTF-8, or holding a NUL byte, is TextNotUTF8.
func TextProblem(text string) string {
	if len(text) > MaxFileBytes {
		return TextTooLarge
	}
	if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return TextNotUTF8
	}
	return ""
}

// EncodedTextLen is the bytes text takes in a sync result's JSON, its
// quotes left out: what it costs against MaxPlaybookTextsBytes. The
// encoder escapes control characters, <, >, & and U+2028/U+2029.
func EncodedTextLen(text string) int {
	// A string always encodes (invalid UTF-8 becomes U+FFFD), so there is
	// no error to handle.
	raw, _ := json.Marshal(text)
	return len(raw) - 2
}
