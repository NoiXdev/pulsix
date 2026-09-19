package notify

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxNameRunes      = 100
	maxRecipients     = 20
	maxAddressBytes   = 254 // RFC 5321 §4.5.3.1.3
	maxURLLength      = 2048
	maxHeaders        = 20
	maxHeaderValue    = 4096
	minSigningSecret  = 16
	maxSigningSecret  = 256
	maxTemplateLength = 16 << 10
)

// headerName is RFC 9110's token: the characters a header name may hold.
var headerName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

// reservedHeader reports whether a (lower-case) header name is set by the
// transport; the X-Pulsix- prefix is Pulsix's own (checkHeader). An
// operator's header must replace neither.
func reservedHeader(lower string) bool {
	switch lower {
	case "host", "content-length", "transfer-encoding", "connection", "upgrade", "te", "trailer",
		"keep-alive", "proxy-authorization", "proxy-connection":
		return true
	}
	return false
}

func normalizeName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || utf8.RuneCountInString(n) > maxNameRunes {
		return "", fmt.Errorf("%w: name must be 1-%d characters", ErrInvalidInput, maxNameRunes)
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: name must not contain control characters", ErrInvalidInput)
		}
	}
	return n, nil
}

// normalizeEmail checks the recipient list: 1-20 plain addresses
// (no display names), duplicates dropped, order kept.
func normalizeEmail(in *EmailConfig) (EmailConfig, error) {
	if in == nil || len(in.Recipients) == 0 {
		return EmailConfig{}, fmt.Errorf("%w: an e-mail channel needs at least one recipient", ErrInvalidInput)
	}
	if len(in.Recipients) > maxRecipients {
		return EmailConfig{}, fmt.Errorf("%w: at most %d recipients", ErrInvalidInput, maxRecipients)
	}
	out := EmailConfig{Recipients: make([]string, 0, len(in.Recipients))}
	seen := map[string]bool{}
	for _, raw := range in.Recipients {
		r := strings.TrimSpace(raw)
		addr, err := mail.ParseAddress(r)
		if err != nil || addr.Name != "" || addr.Address != r || len(r) > maxAddressBytes {
			return EmailConfig{}, fmt.Errorf("%w: %q is not a plain e-mail address of at most %d characters", ErrInvalidInput, r, maxAddressBytes)
		}
		key := strings.ToLower(r)
		if !seen[key] {
			seen[key] = true
			out.Recipients = append(out.Recipients, r)
		}
	}
	return out, nil
}

// normalizeMethod defaults to POST and accepts POST or PUT.
func normalizeMethod(m string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(m)) {
	case "", "POST":
		return "POST", nil
	case "PUT":
		return "PUT", nil
	}
	return "", fmt.Errorf("%w: method must be POST or PUT", ErrInvalidInput)
}

// checkURL validates a webhook URL and returns its display form. Only
// syntax is checked here: the destination address is checked when a
// request is actually made (internal/outbound), because a name can
// resolve differently by then.
func checkURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil, len(raw) > maxURLLength:
		return "", fmt.Errorf("%w: url is not a valid URL of at most %d characters", ErrInvalidInput, maxURLLength)
	case u.Scheme != "http" && u.Scheme != "https":
		return "", fmt.Errorf("%w: url must start with http:// or https://", ErrInvalidInput)
	case u.Host == "" || u.Hostname() == "":
		return "", fmt.Errorf("%w: url needs a host", ErrInvalidInput)
	case u.User != nil:
		return "", fmt.Errorf("%w: url must not carry a user name or password; put credentials in a header", ErrInvalidInput)
	case u.Fragment != "":
		return "", fmt.Errorf("%w: url must not carry a fragment", ErrInvalidInput)
	}
	display := u.Scheme + "://" + u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		display += "/…"
	}
	return display, nil
}

// checkHeader validates one header name and, when given, its value.
func checkHeader(name string, value *string) error {
	if !headerName.MatchString(name) {
		return fmt.Errorf("%w: %q is not a valid header name", ErrInvalidInput, name)
	}
	lower := strings.ToLower(name)
	if reservedHeader(lower) || strings.HasPrefix(lower, "x-pulsix-") {
		return fmt.Errorf("%w: header %s is set by Pulsix and cannot be configured", ErrInvalidInput, name)
	}
	if value != nil {
		if len(*value) > maxHeaderValue {
			return fmt.Errorf("%w: the value of header %s must be one line of at most %d characters", ErrInvalidInput, name, maxHeaderValue)
		}
		if !validHeaderValue(*value) {
			return fmt.Errorf("%w: the value of header %s must be one line of printable characters", ErrInvalidInput, name)
		}
	}
	return nil
}

// validHeaderValue reports whether every byte of v is safe to send as an
// HTTP header field value: visible ASCII and space/tab, never a control
// character (which includes CR and LF — no header splitting) or DEL. This
// is the rule net/http's transport enforces at send time (httpguts); a
// value that fails it is refused here instead of failing at delivery.
func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '\t' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

func checkSigningSecret(secret string) error {
	if n := len(secret); n < minSigningSecret || n > maxSigningSecret {
		return fmt.Errorf("%w: the signing secret must be %d-%d characters", ErrInvalidInput, minSigningSecret, maxSigningSecret)
	}
	return nil
}
