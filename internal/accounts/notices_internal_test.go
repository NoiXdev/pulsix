package accounts

import (
	"fmt"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/locale"
)

// TestNoticeTextsCoverEveryLocaleAndKind: every supported language has a
// subject and a body for every mail this package sends, and every body
// takes exactly the one link it is formatted with. A kind that exists in
// one language only fails here too.
func TestNoticeTextsCoverEveryLocaleAndKind(t *testing.T) {
	kinds := map[noticeKind]bool{
		noticeTemporaryPassword: true,
		noticeSecondFactorReset: true,
		noticeDeactivated:       true,
		noticeResetLink:         true,
	}
	for lang, texts := range noticeTexts {
		if !locale.Valid(lang) {
			t.Errorf("texts for %q, which is not a supported language", lang)
		}
		for kind := range texts {
			kinds[kind] = true
		}
	}
	const link = "https://pulsix.example/some/link"
	for _, lang := range locale.Supported() {
		for kind := range kinds {
			text, ok := noticeTexts[lang][kind]
			if !ok {
				t.Errorf("%s/%s: no text", lang, kind)
				continue
			}
			if strings.TrimSpace(text[0]) == "" || strings.TrimSpace(text[1]) == "" {
				t.Errorf("%s/%s: subject %q, body %q; want both non-empty", lang, kind, text[0], text[1])
			}
			if body := fmt.Sprintf(text[1], link); !strings.Contains(body, link) || strings.Contains(body, "%!") {
				t.Errorf("%s/%s: the body does not take exactly the link: %q", lang, kind, body)
			}
		}
	}
}
