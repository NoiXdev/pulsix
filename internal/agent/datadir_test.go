// What the agent does when its own data directory is not what it expects:
// a path taken by something else, a key file that is not a key. All of it
// is one operational situation — a corrupted or hand-edited
// PULSIX_AGENT_DATA_DIR — and every path here already had the handling;
// what it did not have was a test, which is why internal/agent's coverage
// sat close enough to its 90 % floor for an unrelated timing race to push
// it under (backlog 3.29).
package agent

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// takePath replaces the file the agent expects at dir/name with a
// directory, so a write to it fails at the rename and a read fails with
// EISDIR rather than "not found". The same idiom the identity tests
// already use, and the closest a test gets to "something else owns this
// path" without depending on ownership or the umask.
func takePath(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	_ = os.Remove(p)
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadIdentityPropagatesAKeyItCannotRead: a missing key file means
// "not enrolled yet" and the loop enrolls. A key file that cannot be READ
// means something is wrong with the data directory, and answering "not
// enrolled" to that would re-enroll over a host that already has an
// identity — so it is an error, not an absence.
func TestLoadIdentityPropagatesAKeyItCannotRead(t *testing.T) {
	dir := t.TempDir()
	key, csrPEM := newTestKey(t)
	certPEM, caPEM := issueAgentCert(t, csrPEM)
	if err := SaveEnrollment(dir, key, certPEM, caPEM, "host-1"); err != nil {
		t.Fatal(err)
	}
	// The certificate still reads: this is the key's own error path, one
	// readOptional later, which the certificate's would otherwise mask.
	takePath(t, dir, keyFileName)

	id, err := LoadIdentity(dir)
	if err == nil {
		t.Fatalf("LoadIdentity over an unreadable key = (%v, nil), want an error", id)
	}
	if id != nil {
		t.Fatalf("LoadIdentity returned an identity alongside its error: %v", id)
	}
}

// TestSaveEnrollmentRefusesADataDirThatIsAFile: the directory cannot be
// created because the path is taken. This is what an operator pointing
// PULSIX_AGENT_DATA_DIR at a file gets, and it has to be the enrollment's
// error rather than a panic three writes later.
func TestSaveEnrollmentRefusesADataDirThatIsAFile(t *testing.T) {
	parent := t.TempDir()
	notADir := filepath.Join(parent, "data")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, csrPEM := newTestKey(t)
	certPEM, caPEM := issueAgentCert(t, csrPEM)

	err := SaveEnrollment(notADir, key, certPEM, caPEM, "host-1")
	if err == nil {
		t.Fatal("SaveEnrollment into a file path = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "create data dir") {
		t.Fatalf("SaveEnrollment error = %v, want it to name the directory it could not create", err)
	}
}

// TestSaveEnrollmentReportsWhichFileItCouldNotWrite: the three writes are
// sequential and each has its own wrapped error, so a half-written
// enrollment says which file it stopped at. A shared message would leave
// an operator guessing at which of four paths in the data directory is
// the broken one.
func TestSaveEnrollmentReportsWhichFileItCouldNotWrite(t *testing.T) {
	tests := []struct {
		name  string
		taken string
	}{
		{"the certificate", certFileName},
		{"the metadata", metaFileName},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			key, csrPEM := newTestKey(t)
			certPEM, caPEM := issueAgentCert(t, csrPEM)
			// The writes before this one still succeed, so the error can
			// only come from the file the test took.
			takePath(t, dir, tc.taken)

			err := SaveEnrollment(dir, key, certPEM, caPEM, "host-1")
			if err == nil {
				t.Fatalf("SaveEnrollment with %s taken = nil error, want an error", tc.taken)
			}
			if !strings.Contains(err.Error(), tc.taken) {
				t.Fatalf("SaveEnrollment error = %v, want it to name %s", err, tc.taken)
			}
		})
	}
}

// TestIdentityReplaceReportsWhichFileItCouldNotWrite: the same for a
// renewal, and with one more thing to check. Replace persists both files
// before it swaps the in-memory certificate, precisely so a restart never
// comes back to an older certificate than the one the process is using —
// a failed write must therefore leave the identity on its OLD
// certificate, not half-way onto the new one.
func TestIdentityReplaceReportsWhichFileItCouldNotWrite(t *testing.T) {
	for _, taken := range []string{certFileName, metaFileName} {
		t.Run(taken, func(t *testing.T) {
			dir := t.TempDir()
			key, csrPEM := newTestKey(t)
			certPEM, caPEM := issueAgentCert(t, csrPEM)
			if err := SaveEnrollment(dir, key, certPEM, caPEM, "host-1"); err != nil {
				t.Fatal(err)
			}
			id, err := LoadIdentity(dir)
			if err != nil || id == nil {
				t.Fatalf("LoadIdentity: %v", err)
			}
			before := id.Leaf()
			renewedPEM, _ := issueAgentCert(t, csrPEM)
			takePath(t, dir, taken)

			err = id.Replace(renewedPEM)

			if err == nil {
				t.Fatalf("Replace with %s taken = nil error, want an error", taken)
			}
			if !strings.Contains(err.Error(), taken) {
				t.Fatalf("Replace error = %v, want it to name %s", err, taken)
			}
			if id.Leaf() != before {
				t.Fatal("Replace swapped the in-memory certificate despite failing to persist it")
			}
		})
	}
}

// TestIdentityValidWithoutALeaf: Valid is asked of whatever LoadIdentity
// returned, and the loop decides between "stream with this" and "enroll"
// on the answer. An identity with no parsed certificate must answer no —
// answering yes would send the loop to dial with nothing to present.
func TestIdentityValidWithoutALeaf(t *testing.T) {
	if (&Identity{}).Valid(time.Now()) {
		t.Fatal("an identity with no certificate reported itself valid")
	}
	var absent *Identity
	if absent.Valid(time.Now()) {
		t.Fatal("a nil identity reported itself valid")
	}
}

// TestLoadOrGenerateKeyRefusesAKeyThatIsNotAnECKey: a well-formed PEM
// block holding something else — a PKCS#8 or RSA key pasted over it. The
// existing garbage test stops one step earlier, at a block that does not
// decode at all; this is the step after, and it must still refuse rather
// than generate a replacement, because overwriting the key orphans a
// certificate the manager still trusts for this host.
func TestLoadOrGenerateKeyRefusesAKeyThatIsNotAnECKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, keyFileName)
	notAnECKey := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("this decodes, and is not a key")})
	if err := os.WriteFile(path, notAnECKey, 0o600); err != nil {
		t.Fatal(err)
	}

	key, err := LoadOrGenerateKey(path)

	if err == nil {
		t.Fatal("LoadOrGenerateKey over a non-EC key = nil error, want an error")
	}
	if key != nil {
		t.Fatal("LoadOrGenerateKey returned a key alongside its error")
	}
	// And the file on disk is untouched: a regenerated key here would be
	// silent and unrecoverable.
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(notAnECKey) {
		t.Fatalf("the key file was rewritten: %q", after)
	}
	if _, err := x509.ParseECPrivateKey([]byte("this decodes, and is not a key")); err == nil {
		t.Fatal("the fixture parses as an EC key, so this test proves nothing")
	}
}
