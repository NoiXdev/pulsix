// Package locale names the GUI languages Pulsix ships and decides which one
// applies. The server only stores and resolves the code; every translated
// string lives in the SPA.
package locale

const (
	// EN is English, the fallback of last resort.
	EN = "en"
	// DE is German.
	DE = "de"
)

// Supported lists the supported language codes, English first. The slice is
// the caller's own.
func Supported() []string { return []string{EN, DE} }

// Valid reports whether s is exactly one of the supported language codes.
func Valid(s string) bool { return s == EN || s == DE }

// Resolve picks the language to show: the user's own choice if it is a
// supported one, else the instance default if that is, else English.
func Resolve(user *string, instanceDefault string) string {
	if user != nil && Valid(*user) {
		return *user
	}
	if Valid(instanceDefault) {
		return instanceDefault
	}
	return EN
}
