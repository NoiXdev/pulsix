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
	"unicode/utf8"

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
	ProblemHeaderCredentials = "must not set Authorization, Proxy-Authorization, Cookie or a header whose name suggests a credential (token, secret, API or access key, password, session, auth): credentials belong in the check's secret"
	ProblemHeaderRouting     = "must not set X-Forwarded-*, Forwarded, X-Real-IP, X-Original-URL, X-Rewrite-URL or a method override: a target may trust them for the client's address, the path or the method"
	ProblemMediaType         = "must be a media type such as application/json or text/plain; charset=utf-8"
	ProblemBodyText          = "must be UTF-8 text without NUL characters"
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
// definition: the check's secret sends them (its header and value form
// sends any one header).
var credentialHeaders = map[string]bool{"Authorization": true, "Proxy-Authorization": true, "Cookie": true}

// credentialWords in a header name (case ignored) suggest it carries a
// credential: X-Api-Key, X-Api_Key, X-Auth-Token, Private-Token,
// X-Client-Secret, X-Access-Key, X-Session-Id. A heuristic, never
// complete; the form's hint points to the secret either way.
var credentialWords = []string{
	"token", "secret", "apikey", "api-key", "api_key", "password", "access-key", "access_key", "session",
}

// credentialSegments, as a whole segment of a name split at '-' and '_'
// (case ignored), suggest a credential: X-Auth, X-Auth-User, Api-Auth,
// but not X-Author.
var credentialSegments = []string{"auth"}

// routingHeaders are the headers a target or its proxy may trust for the
// client's address, the original path or the method (canonical form);
// X-Forwarded and every X-Forwarded-* name are refused as well.
var routingHeaders = map[string]bool{
	"Forwarded": true, "X-Real-Ip": true, "X-Original-Url": true, "X-Rewrite-Url": true,
	"X-Http-Method-Override": true, "X-Http-Method": true, "X-Method-Override": true,
}

// credentialLike reports whether canonical is a credential header or
// suggests one.
func credentialLike(canonical string) bool {
	if credentialHeaders[canonical] {
		return true
	}
	lower := strings.ToLower(canonical)
	if slices.ContainsFunc(credentialWords, func(w string) bool { return strings.Contains(lower, w) }) {
		return true
	}
	segments := strings.FieldsFunc(lower, func(r rune) bool { return r == '-' || r == '_' })
	return slices.ContainsFunc(segments, func(seg string) bool { return slices.Contains(credentialSegments, seg) })
}

// routing reports whether canonical is a routing or override header.
func routing(canonical string) bool {
	return routingHeaders[canonical] || canonical == "X-Forwarded" || strings.HasPrefix(canonical, "X-Forwarded-")
}

// HeaderProblem describes why entry is not a headers entry a check may
// send, or returns "" when it is one: "Name: value" split at the first
// colon, the name a token of at most 128 bytes and not one the probe
// controls or that carries credentials, the value without control
// characters other than tab, and UTF-8. Routing and override headers
// (routing) are refused too. Spaces and tabs around the value are not
// part of it; the value may be empty.
func HeaderProblem(entry string) string {
	name, value, found := strings.Cut(entry, ":")
	if !found || len(name) > maxHeaderNameLen || !httpguts.ValidHeaderFieldName(name) {
		return ProblemHeaderShape
	}
	if !httpguts.ValidHeaderFieldValue(value) || !utf8.ValidString(value) {
		return ProblemHeaderValue
	}
	if problem := NameProblem(name); problem != "" {
		return problem
	}
	if credentialLike(http.CanonicalHeaderKey(name)) {
		return ProblemHeaderCredentials
	}
	return ""
}

// NameProblem describes why name (a valid header name, any case) is a
// header no configuration may set, or returns "" when it may be: one the
// probe controls (ProblemHeaderReserved), Content-Type
// (ProblemHeaderContentType) or a routing or override header
// (ProblemHeaderRouting). It leaves credential headers alone: the check's
// secret sends them, and its header follows this rule.
func NameProblem(name string) string {
	canonical := http.CanonicalHeaderKey(name)
	switch {
	case reserved[canonical]:
		return ProblemHeaderReserved
	case canonical == "Content-Type":
		return ProblemHeaderContentType
	case routing(canonical):
		return ProblemHeaderRouting
	}
	return ""
}

// BodyProblem describes why body is not a request body a check may send,
// or returns "" when it is one: UTF-8 text without NUL (which the
// database cannot store either).
func BodyProblem(body string) string {
	if !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return ProblemBodyText
	}
	return ""
}

// Writes reports whether a request of method, with or without a body,
// may change something on its target: POST, PUT, PATCH or DELETE, or any
// request with a body. The manager sends such a request only while its
// operator allows it (PULSIX_PROBE_WRITE_METHODS).
func Writes(method string, hasBody bool) bool {
	return hasBody || method == http.MethodPost || method == http.MethodPut ||
		method == http.MethodPatch || method == http.MethodDelete
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
