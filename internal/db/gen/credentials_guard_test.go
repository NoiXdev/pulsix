package gen

import (
	"regexp"
	"testing"
)

// secretColumn matches the credentials table's sealed-secret column as a
// whole word, so a hit on "secret" in getCredential or listCredentials'
// SQL text can only be the column itself, not a coincidental substring.
var secretColumn = regexp.MustCompile(`(?i)\bsecret\b`)

// TestCredentialMetadataQueriesNeverSelectSecret is a regression guard for
// backlog 2.17: GetCredential and ListCredentials are metadata reads used
// by the credentials list and detail pages and by every other package that
// looks up a credential for validation (sshconf, hosts, jobs); neither
// should ever pull the sealed secret ciphertext off the wire just to have
// Go code ignore it. The one query allowed to select it is
// GetCredentialSecret, used only by the worker's claim path
// (credentials.SQLStore.LoadSecret) — checked here too, so a future edit
// that narrows it further doesn't silently defeat this guard.
//
// This inspects the literal SQL text sqlc embedded in the generated query
// constants, which is why it lives in this package instead of
// internal/credentials: those constants are unexported.
func TestCredentialMetadataQueriesNeverSelectSecret(t *testing.T) {
	if secretColumn.MatchString(getCredential) {
		t.Errorf("GetCredential selects the secret column:\n%s", getCredential)
	}
	if secretColumn.MatchString(listCredentials) {
		t.Errorf("ListCredentials selects the secret column:\n%s", listCredentials)
	}
	if !secretColumn.MatchString(getCredentialSecret) {
		t.Fatal("GetCredentialSecret no longer selects the secret column — the guard above would no longer mean anything if this query stopped being the one place it is read")
	}
}
