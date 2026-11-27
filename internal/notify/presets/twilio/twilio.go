// Package twilio renders a webhook channel's message as an SMS through
// Twilio's Messages API (backlog 4.8). Pulsix builds the URL from the
// account SID; the auth token is the channel's sealed secret and goes out
// as basic auth. Every message is a billed SMS, so the text is bounded to
// two segments.
package twilio

import (
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/noixdev/pulsix/internal/notify"
)

const production = "https://api.twilio.com"

var (
	accountSID = regexp.MustCompile(`^AC[0-9a-fA-F]{32}$`)
	authToken  = regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
	// phone is E.164: a plus, a country code, at most 15 digits.
	phone = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
	// senderID is an alphanumeric sender: up to 11 letters, digits and
	// spaces, at least one letter.
	senderID = regexp.MustCompile(`^[A-Za-z0-9 ]{1,11}$`)
	letter   = regexp.MustCompile(`[A-Za-z]`)
)

type preset struct{ base string }

// New returns the Twilio SMS preset posting under base ("" is Twilio's
// own API; tests point it at a fake).
func New(base string) notify.Preset {
	if base == "" {
		base = production
	}
	return preset{base: strings.TrimRight(base, "/")}
}

// CheckURL is never asked: the preset builds its URL.
func (preset) CheckURL(*url.URL) error { return nil }

// CheckSettings requires the account, one recipient and a sender.
func (preset) CheckSettings(s notify.PresetSettings) (notify.PresetSettings, error) {
	s.AccountSID, s.From, s.To = strings.TrimSpace(s.AccountSID), strings.TrimSpace(s.From), strings.TrimSpace(s.To)
	switch {
	case !accountSID.MatchString(s.AccountSID):
		return s, errors.New("a Twilio account SID is AC followed by 32 hexadecimal digits")
	case !phone.MatchString(s.To):
		return s, errors.New("the recipient (to) is one phone number in international format, such as +4915112345678")
	case !phone.MatchString(s.From) && !isSenderID(s.From):
		return s, errors.New("the sender (from) is a Twilio phone number in international format, or an alphanumeric sender ID of up to 11 letters, digits and spaces")
	}
	return s, s.Only("Twilio", "account_sid", "from", "to")
}

// isSenderID reports whether from is an alphanumeric sender ID.
func isSenderID(from string) bool { return senderID.MatchString(from) && letter.MatchString(from) }

// Endpoint is the account's Messages resource.
func (p preset) Endpoint(s notify.PresetSettings) string {
	return p.base + "/2010-04-01/Accounts/" + s.AccountSID + "/Messages.json"
}

// SecretScope: the auth token belongs to the account, so a new account
// SID needs its token again (review M2).
func (preset) SecretScope(s notify.PresetSettings) (string, string) {
	return "account_sid", s.AccountSID
}

// SecretNames is the account's auth token.
func (preset) SecretNames() []string { return []string{"auth_token"} }

// CheckSecret checks the shape; its error never repeats the value.
func (preset) CheckSecret(_, value string) error {
	if !authToken.MatchString(value) {
		return errors.New("a Twilio auth token is 32 letters and digits")
	}
	return nil
}

// Render is one form-encoded SMS.
func (preset) Render(ev notify.Event, t notify.Target) ([]notify.Request, error) {
	s := t.Settings
	v := url.Values{}
	v.Set("To", s.To)
	v.Set("From", s.From)
	v.Set("Body", text(notify.Compose(ev)))
	auth := base64.StdEncoding.EncodeToString([]byte(s.AccountSID + ":" + t.Secrets["auth_token"]))
	return []notify.Request{{
		ContentType: "application/x-www-form-urlencoded",
		Header:      [][2]string{{"Authorization", "Basic " + auth}},
		Body:        []byte(v.Encode()),
	}}, nil
}

// text is the SMS: the title, the lines that fit, a note for the rest
// and the link to Pulsix when it is short enough to be worth its room,
// one per line, within two segments of the encoding the text needs.
func text(m notify.Message) string {
	head := fold.Replace(m.Title)
	lines := make([]string, len(m.Lines))
	for i, l := range m.Lines {
		lines[i] = fold.Replace(l.Text)
		if l.Label != "" {
			lines[i] = fold.Replace(l.Label) + ": " + lines[i]
		}
	}
	size, limit := measure(strings.Join(append([]string{head, m.URL}, lines...), "\n"))
	head = clipTo(head, limit*2/3, size)
	link, linkCost := "", 0
	if m.URL != "" && size(m.URL) <= limit/3 && size(head)+1+size(m.URL) <= limit {
		link, linkCost = m.URL, size(m.URL)+1
	}
	noteSize := func(s string) int { return size(fold.Replace(s)) }
	kept, more := notify.Fit(lines, limit-size(head)-linkCost, noteSize, m.Omitted)
	parts := append([]string{head}, kept...)
	if more > 0 {
		parts = append(parts, fold.Replace(notify.MoreNote(more)))
	}
	if link != "" {
		parts = append(parts, link)
	}
	out := strings.Join(parts, "\n")
	for size(out) > limit && len(parts) > 1 {
		// Only when not even the note fits beside the title: drop from
		// the end.
		parts = parts[:len(parts)-1]
		out = strings.Join(parts, "\n")
	}
	return out
}
