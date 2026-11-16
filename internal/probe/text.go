package probe

import (
	"crypto/x509"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds of the manager's check-results contract (internal/checks
// MaxItemLen and MaxLabelLen); a test keeps them equal. This package does
// not import internal/checks, which pulls in the database layer.
const (
	MaxItemLen  = 512
	MaxLabelLen = 512
)

// EmptyText is the label of a text that is empty once cleaned.
const EmptyText = "(empty)"

// CleanText turns text a target sent (a device's string, a JSON string)
// into text an item or a label may carry: invalid UTF-8 replaced, a tab
// turned into a space, other control characters and line or paragraph
// separators removed, trimmed. Devices pad names with NULs and put
// anything into their descriptions.
func CleanText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if isControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// ShortLabelLen bounds a label quoting what a target sent where a whole
// MaxLabelLen would crowd a table or an alert: a certificate's issuer, an
// assertion's actual value.
const ShortLabelLen = 128

// Label is s cleaned and cut to MaxLabelLen bytes on a rune boundary, like
// the script plugin's output; empty becomes EmptyText.
func Label(s string) string {
	return cutLabel(s, MaxLabelLen)
}

// ShortLabel is Label cut to ShortLabelLen bytes.
func ShortLabel(s string) string {
	return cutLabel(s, ShortLabelLen)
}

func cutLabel(s string, maxLen int) string {
	s = CleanText(s)
	if len(s) > maxLen {
		n := maxLen
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = strings.TrimSpace(s[:n])
	}
	if s == "" {
		return EmptyText
	}
	return s
}

// IssuerLabel names the issuer of cert: its Common Name, else its first
// Organization, else its distinguished name, as a ShortLabel. Each is
// taken only when it is not blank once cleaned; nil, or an issuer without
// any, is EmptyText.
func IssuerLabel(cert *x509.Certificate) string {
	if cert == nil {
		return EmptyText
	}
	candidates := []string{cert.Issuer.CommonName}
	if len(cert.Issuer.Organization) > 0 {
		candidates = append(candidates, cert.Issuer.Organization[0])
	}
	candidates = append(candidates, cert.Issuer.String())
	for _, c := range candidates {
		if CleanText(c) != "" {
			return ShortLabel(c)
		}
	}
	return EmptyText
}

// ReportableItem reports whether the manager accepts item: not empty, at
// most MaxItemLen bytes, valid UTF-8, no control character or line
// separator (the agent's checks.ReportableItem, plus not empty: an item of
// "" would be the whole-host item).
func ReportableItem(item string) bool {
	return item != "" && len(item) <= MaxItemLen && utf8.ValidString(item) && strings.IndexFunc(item, isControl) < 0
}

func isControl(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
}
