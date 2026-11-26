// Package httpreq holds the rules of the request an http check sends
// (backlog 3.84): which methods it may use, which of them carry a body,
// how a headers entry ("Name: value") is read and which header names are
// not the operator's to set, and the shape of a content type. The
// descriptor's validation (internal/checkplugins) and the probe
// (internal/probe/http) both read the parameters with this package, so
// what passes validation is what the probe sends. It imports nothing of
// Pulsix's.
package httpreq

import (
	"net/http"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// Bounds of the request parameters.
const (
	// MaxHeaders bounds the headers list.
	MaxHeaders = 16
	// MaxHeaderLen bounds one headers entry, name and value together.
	MaxHeaderLen = 1024
	// maxHeaderNameLen bounds a header's name.
	maxHeaderNameLen = 128
	// MaxBodyLen bounds the request body in bytes.
	MaxBodyLen = 8 << 10
	// MaxContentTypeLen bounds content_type.
	MaxContentTypeLen = 255
	// DefaultContentType is the Content-Type of a body when content_type
	// is absent.
	DefaultContentType = "application/json"
)

// Methods are the request methods a check may use, in the order a form
// offers them; GET is the default.
var Methods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions,
}

// KnownMethod reports whether m is one of Methods (case included).
func KnownMethod(m string) bool { return slices.Contains(Methods, m) }

// TakesBody reports whether a request of method m may carry a body: POST,
// PUT and PATCH. The others have no defined meaning for one (RFC 9110).
func TakesBody(m string) bool {
	return m == http.MethodPost || m == http.MethodPut || m == http.MethodPatch
}

// The problems a headers entry or a content type can have, phrased to
// follow "<param> entries " or "<param> ".
const (
	ProblemHeaderShape       = "must be Name: value, the name at most 128 letters, digits or !#$%&'*+-.^_`|~"
	ProblemHeaderValue       = "must have a value without control characters such as line breaks"
	ProblemHeaderReserved    = "must not set Host, Content-Length, Transfer-Encoding, Connection, User-Agent, Keep-Alive, Proxy-Connection, TE, Trailer, Upgrade, Expect or Accept-Encoding: the probe sets them"
	ProblemHeaderContentType = "must not set Content-Type: use content_type"
	ProblemHeaderCredentials = "must not set Authorization, Proxy-Authorization or Cookie: credentials belong in the check's secret"
	ProblemMediaType         = "must be a media type such as application/json or text/plain; charset=utf-8"
)

// reserved are the headers the probe or its transport controls: framing,
// connection management, the probe's own identity and the transparent
// decompression of the response.
var reserved = map[string]bool{
	"Host": true, "Content-Length": true, "Transfer-Encoding": true, "Connection": true, "User-Agent": true,
	"Keep-Alive": true, "Proxy-Connection": true, "Te": true, "Trailer": true, "Upgrade": true,
	"Expect": true, "Accept-Encoding": true,
}

// credentialHeaders carry credentials, which never stand in a check's
// definition: the check's secret sends them.
var credentialHeaders = map[string]bool{"Authorization": true, "Proxy-Authorization": true, "Cookie": true}

// HeaderProblem describes why entry is not a headers entry a check may
// send, or returns "" when it is one: "Name: value" split at the first
// colon, the name a token of at most 128 bytes and not one the probe
// controls or that carries credentials, the value without control
// characters other than tab. Spaces and tabs around the value are not
// part of it; the value may be empty.
func HeaderProblem(entry string) string {
	name, value, found := strings.Cut(entry, ":")
	if !found || len(name) > maxHeaderNameLen || !httpguts.ValidHeaderFieldName(name) {
		return ProblemHeaderShape
	}
	if !httpguts.ValidHeaderFieldValue(value) {
		return ProblemHeaderValue
	}
	canonical := http.CanonicalHeaderKey(name)
	switch {
	case reserved[canonical]:
		return ProblemHeaderReserved
	case canonical == "Content-Type":
		return ProblemHeaderContentType
	case credentialHeaders[canonical]:
		return ProblemHeaderCredentials
	}
	return ""
}

// ParseHeader reads a headers entry: the canonical name and the trimmed
// value, ok false when HeaderProblem refuses it.
func ParseHeader(entry string) (name, value string, ok bool) {
	if HeaderProblem(entry) != "" {
		return "", "", false
	}
	name, value, _ = strings.Cut(entry, ":")
	return http.CanonicalHeaderKey(name), strings.Trim(value, " \t"), true
}

// mediaType is RFC 9110's media-type: type "/" subtype *( OWS ";" OWS
// parameter ), a parameter token "=" ( token / quoted-string ), the
// quoted string without control characters.
var mediaType = regexp.MustCompile(`^` + token + `/` + token +
	`(?:[ \t]*;[ \t]*` + token + `=(?:` + token + `|"(?:[^"\\\x00-\x08\x0a-\x1f\x7f]|\\[^\x00-\x08\x0a-\x1f\x7f])*"))*$`)

const token = "[!#$%&'*+.^_`|~0-9A-Za-z-]+"

// MediaTypeProblem describes why s is not a content type a check may
// send, or returns "" when it is one.
func MediaTypeProblem(s string) string {
	if !mediaType.MatchString(s) {
		return ProblemMediaType
	}
	return ""
}
