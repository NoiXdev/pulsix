// Redaction of run output (final review I4): ansible-runner's events carry
// module results and display text verbatim, and a task can print anything
// it can reach — a become password, a secret extra var, a key file. Every
// event is reduced to what the run view needs and every secret value the
// claim revealed is replaced by [redacted] BEFORE the event leaves the
// runner (and so before the loop's size bound truncates it: a secret is
// always replaced whole, never cut into a surviving prefix).
package runner

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// redactedMarker replaces every secret occurrence.
const redactedMarker = "[redacted]"

// minRedactLen is the shortest secret value redacted: shorter values would
// shred ordinary output (a 2-character password matches everywhere) and
// cannot be protected in free text anyway.
const minRedactLen = 4

// minRedactLineLen is the shortest single line of a multi-line secret (a
// private key) redacted on its own, for output that shows the key line by
// line.
const minRedactLineLen = 16

// redactor removes a claim's secret values from event text.
type redactor struct {
	// values are the literal strings to replace, longest first so a secret
	// that contains another is replaced whole.
	values []string
}

// newRedactor collects the claim's secrets: the manager's redact list
// (secret extra vars, secret inventory vars, one-time logins, ...) plus
// every part of every credential the claim ships — defense in depth for a
// claim whose list is incomplete. Each value is also redacted in its
// Python repr() spellings (backlog 7.53, see reprSpellings), in its YAML
// single-quoted spelling (to_yaml and to_nice_yaml) and its percent-encoded
// spellings (urlencode's path and query forms; backlog 7.104, see
// encodedSpellings); each of these, and the value itself, in its
// JSON-escaped spellings (display text shows module results as JSON, see
// jsonSpellings) and escaped once more for JSON text shown as a JSON string
// (nestedJSONSpellings); in its base64 spellings (backlog 2.48, see
// base64Spellings) and, for a multi-line value, line by line.
func newRedactor(job *workerpb.Job) *redactor {
	set := map[string]bool{}
	add := func(v string) {
		if len(v) < minRedactLen {
			return
		}
		set[v] = true
		// The value as Python's repr() prints it (backlog 7.53), as YAML
		// single-quotes it and as it is percent-encoded (backlog 7.104) are
		// further base texts a run can show, and each base is in turn shown
		// as JSON text.
		for _, base := range append(append([]string{v}, reprSpellings(v)...), encodedSpellings(v)...) {
			set[base] = true
			for _, esc := range jsonSpellings(base) {
				set[esc] = true
			}
			for _, esc := range nestedJSONSpellings(base) {
				set[esc] = true
			}
		}
		for _, enc := range base64Spellings(v) {
			set[enc] = true
		}
		if strings.Contains(v, "\n") {
			for _, line := range strings.Split(v, "\n") {
				if line = strings.TrimSpace(line); len(line) >= minRedactLineLen {
					set[line] = true
				}
			}
		}
	}
	for _, v := range job.GetRedact() {
		add(v)
	}
	for _, raw := range job.GetCredentials() {
		var s credentials.Secret
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			continue
		}
		add(s.PrivateKey)
		add(s.Password)
		add(s.Token)
	}
	r := &redactor{values: make([]string, 0, len(set))}
	for v := range set {
		r.values = append(r.values, v)
	}
	slices.SortFunc(r.values, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	return r
}

// encodedSpellings is the YAML and URL spellings of v a run can print (backlog
// 7.104), each only when it differs from v. Ansible's to_yaml and
// to_nice_yaml write a string that cannot be a plain scalar (a leading '@',
// '!', '%' or '`', an embedded ': ') single-quoted with every ' doubled; only
// that body is listed, the quotes around it are not part of the secret.
// Jinja2's urlencode filter percent-encodes through Python's
// quote(safe='/') for a string and through urlencode's query form (Go's
// url.QueryEscape: safe set without '/', ' as '+') for a dict or a for_qs
// call, both with upper-case hex.
func encodedSpellings(v string) []string {
	var out []string
	if strings.Contains(v, "'") {
		out = append(out, strings.ReplaceAll(v, "'", "''"))
	}
	for _, enc := range []string{pyQuote(v), url.QueryEscape(v)} {
		if enc != v && !slices.Contains(out, enc) {
			out = append(out, enc)
		}
	}
	return out
}

// pyQuote is v as Python's urllib.parse.quote(v, safe='/') writes it: every
// byte except ASCII letters, digits and "_.-~/" as %XX with upper-case hex
// (backlog 7.104). Written by hand: url.PathEscape and url.QueryEscape each
// differ from it in the characters they keep.
func pyQuote(v string) string {
	const hex = "0123456789ABCDEF"
	var sb strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '.', c == '-', c == '~', c == '/':
			sb.WriteByte(c)
		default:
			sb.WriteByte('%')
			sb.WriteByte(hex[c>>4])
			sb.WriteByte(hex[c&15])
		}
	}
	return sb.String()
}

// jsonSpellings is every JSON string-body spelling of v a run can print:
// Go's json.Marshal form (HTML-escaping '&', '<' and '>', as the worker's own
// JSON uses) and the form ansible-core's result display prints module
// results in, Python's json.dumps(ensure_ascii=False). That one escapes only
// '"', '\\' and control characters below U+0020 and leaves '&', '<', '>',
// U+2028/U+2029 and non-ASCII text literal, so a secret mixing a quote or
// backslash with '&', '<' or '>' matched neither spelling Go produces and
// reached viewers in the display text (backlog 7.9). It is written by hand
// because no Go encoder setting reproduces Python's choices exactly.
//
// The third is json.dumps' default, ensure_ascii=True, which also writes
// every non-ASCII character as \uXXXX: it is what ansible's to_json filter
// produces, so a secret inside a document a play built with it carries
// that spelling (backlog 7.34).
func jsonSpellings(v string) []string {
	var out []string
	if b, err := json.Marshal(v); err == nil {
		out = append(out, string(b[1:len(b)-1]))
	}
	return append(out, pythonJSON(v, false), pythonJSON(v, true))
}

// nestedJSONSpellings is every spelling of v as a JSON string inside JSON
// text that is itself shown as a JSON string: a module result that holds a
// document (to_json, a module returning JSON as text), printed by the
// result display. Each of jsonSpellings' forms, escaped again by each of
// them (backlog 7.34).
func nestedJSONSpellings(v string) []string {
	var out []string
	for _, once := range jsonSpellings(v) {
		out = append(out, jsonSpellings(once)...)
	}
	return out
}

// pythonJSON is v's string body as Python's json.dumps writes it, with
// ensure_ascii as given: '"', '\\' and the control characters escaped, and
// with ensure_ascii every character past U+007F as a \uXXXX escape too
// (a UTF-16 surrogate pair past U+FFFF), in lower-case hex like Python.
func pythonJSON(v string, ensureASCII bool) string {
	var sb strings.Builder
	for _, r := range v {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(&sb, `\u%04x`, r)
			case ensureASCII && r > 0x7f:
				if r > 0xffff {
					hi, lo := utf16.EncodeRune(r)
					fmt.Fprintf(&sb, `\u%04x\u%04x`, hi, lo)
				} else {
					fmt.Fprintf(&sb, `\u%04x`, r)
				}
			default:
				sb.WriteRune(r)
			}
		}
	}
	return sb.String()
}

// reprSpelling is v's string body as Python's repr() writes it (backlog
// 7.53). A template that renders a dict or list ("cfg: {{ cfg }}") prints
// its string members through repr(), which is neither the plain value nor a
// JSON spelling: the literal is single-quoted unless v holds a single quote
// and no double quote, the chosen quote and the backslash are backslash-escaped, and
// '\n', '\r' and '\t' and every non-printable character are written as
// escapes (\xNN, \uNNNN, \UNNNNNNNN by size) while printable non-ASCII text
// stays literal. A value holding both quote kinds therefore appears as
// Pa"ss\'w0rd, which matches neither the value nor any JSON spelling of it.
func reprSpelling(v string) string {
	quote := '\''
	if strings.Contains(v, "'") && !strings.Contains(v, `"`) {
		quote = '"'
	}
	return reprBody(v, quote)
}

// reprSpellings is every repr() body a secret can appear in. Python picks the
// quote from the WHOLE string it prints, not from the secret: a secret with
// only a single quote, inside a larger value that also holds a double quote,
// is printed in a single-quoted literal with the quote escaped ('it\'s-pw "x"'),
// which reprSpelling alone (double-quoted for that secret) would miss.
func reprSpellings(v string) []string {
	out := []string{reprSpelling(v)}
	if single := reprBody(v, '\''); single != out[0] {
		out = append(out, single)
	}
	return out
}

// reprBody is v written between the given quote characters the way repr()
// escapes it.
func reprBody(v string, quote rune) string {
	var sb strings.Builder
	for _, r := range v {
		switch {
		case r == quote || r == '\\':
			sb.WriteRune('\\')
			sb.WriteRune(r)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\r':
			sb.WriteString(`\r`)
		case r == '\t':
			sb.WriteString(`\t`)
		case unicode.IsPrint(r):
			sb.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&sb, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&sb, `\u%04x`, r)
		default:
			fmt.Fprintf(&sb, `\U%08x`, r)
		}
	}
	return sb.String()
}

// minBase64Core is the shortest run of a secret's bytes whose base64 form
// is redacted on its own (see base64Spellings): 6 bytes encode to 8
// characters, long enough not to match ordinary output by chance.
const minBase64Core = 6

// base64Spellings is every base64 form of v a run can print (backlog
// 2.48). Ansible reaches for base64 routinely — `slurp` returns file
// contents that way, and `b64encode` is an everyday filter — so a secret
// read from a file and shown in a module result leaves the worker intact
// unless its encoded spelling is replaced too.
//
// All four alphabets, because which one appears is not ours to choose:
// standard is what Ansible's own filters emit, the URL alphabet turns up
// wherever a value travels in a URL or a JWT, and either may arrive
// unpadded.
//
// base64 encodes three bytes at a time, so a secret inside a larger buffer
// ("pwd=<secret>" in a slurped file) encodes differently depending on
// where it starts, and its last characters depend on the byte after it.
// The whole-value spellings only cover a secret alone at a 3-byte boundary
// (backlog 7.11). So for each of the three alignments the list also holds
// the secret's aligned core: the bytes from that offset, cut to whole
// 3-byte groups, encoded unpadded. One of the three always sits on a group
// boundary of the surrounding buffer and encodes to the same characters
// there, whatever comes before or after. Replacing it leaves at most a
// few edge bytes of the secret, never the secret. A secret too short for a
// core of minBase64Core bytes keeps only the whole-value spellings.
//
// Only transformations that are REVERSIBLE belong here. A hash of a
// secret is deliberately not in this list: it is not the secret, and
// whether a digest of a password ever reaches a run's output is a guess,
// where base64 has a named source. Guessing costs output — every entry
// is replaced wherever it appears — so the bar is a transformation we
// can name a producer for.
func base64Spellings(v string) []string {
	b := []byte(v)
	out := make([]string, 0, 10)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if s := enc.EncodeToString(b); s != "" && s != v {
			out = append(out, s)
		}
	}
	for skip := 0; skip < 3 && skip < len(b); skip++ {
		core := b[skip:]
		core = core[:len(core)-len(core)%3]
		if len(core) < minBase64Core {
			continue
		}
		for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
			out = append(out, enc.EncodeToString(core))
		}
	}
	return out
}

// text redacts one string.
func (r *redactor) text(s string) string {
	for _, v := range r.values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, redactedMarker)
		}
	}
	return s
}

// value redacts every string inside a decoded JSON value (keys included).
func (r *redactor) value(v any) any {
	switch t := v.(type) {
	case string:
		return r.text(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[r.text(k)] = r.value(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = r.value(e)
		}
		return out
	default:
		return v
	}
}

// eventPayload is the one payload a mapped ansible event is forwarded
// with: the event name, the display text, and the event data the run view
// reads (host, task, module, result) — runner bookkeeping (uuids, counters,
// pids, playbook paths) stays behind. Everything is redacted.
func (r *redactor) eventPayload(ev Event) string {
	var raw struct {
		Event     string         `json:"event"`
		EventData map[string]any `json:"event_data"`
	}
	_ = json.Unmarshal([]byte(ev.Payload), &raw)
	data := map[string]any{}
	for _, k := range []string{"host", "task", "task_action", "res"} {
		if v, ok := raw.EventData[k]; ok {
			data[k] = r.value(v)
		}
	}
	out := map[string]any{"event": raw.Event, "event_data": data}
	if ev.Stdout != "" {
		out["stdout"] = r.text(ev.Stdout)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"event":"` + raw.Event + `"}`
	}
	return string(b)
}

// textPayload is the payload of a display-only event (a banner, the
// recap): {"text": <redacted display text>} — the shape the run view
// renders as a plain output line.
func (r *redactor) textPayload(stdout string) string {
	b, err := json.Marshal(map[string]string{"text": r.text(stdout)})
	if err != nil {
		return `{"text":""}`
	}
	return string(b)
}
