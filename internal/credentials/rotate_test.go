// The rotation orchestration's tests (fakes): Rotate generates a fresh
// keypair per call, enqueues through the port and re-keys a credential
// that nothing references immediately; FinishRotation seals the pending
// key only when every job of the group succeeded. The store-level group
// behavior lives in internal/jobs' integration tests.
package credentials_test

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
)

// rotatingStore is the fake Store capturing the writes the rotation makes.
type rotatingStore struct {
	credentials.Store
	cred       credentials.Credential
	updates    []credentials.Credential
	updateErr  error
	getErr     error
	secretSeal []byte
}

func (s *rotatingStore) Get(_ context.Context, _, _ uuid.UUID) (credentials.Credential, error) {
	if s.getErr != nil {
		return credentials.Credential{}, s.getErr
	}
	return s.cred, nil
}

func (s *rotatingStore) Update(_ context.Context, c credentials.Credential, sealed []byte) (credentials.Credential, error) {
	if s.updateErr != nil {
		return credentials.Credential{}, s.updateErr
	}
	s.updates = append(s.updates, c)
	s.secretSeal = sealed
	return c, nil
}

// fakeRotationEnqueuer records the enqueue calls.
type fakeRotationEnqueuer struct {
	calls []enqueuedRotation
	count int
	err   error
}

type enqueuedRotation struct {
	orgID, credentialID uuid.UUID
	key                 credentials.PendingKey
	actor               *uuid.UUID
}

func (f *fakeRotationEnqueuer) EnqueueRotation(_ context.Context, orgID, credentialID uuid.UUID, key credentials.PendingKey, actor *uuid.UUID) (uuid.UUID, uuid.UUID, int, error) {
	if f.err != nil {
		return uuid.Nil, uuid.Nil, 0, f.err
	}
	f.calls = append(f.calls, enqueuedRotation{orgID: orgID, credentialID: credentialID, key: key, actor: actor})
	return uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), f.count, nil
}

func rotationService(t *testing.T, store credentials.Store, enq credentials.RotationEnqueuer) *credentials.Service {
	t.Helper()
	svc := credentials.NewService(store, box(), time.Now, rand.Reader)
	svc.SetRotationEnqueuer(enq)
	return svc
}

// box is the test's sealing box.
func box() *crypto.Box {
	var key [32]byte
	copy(key[:], "credentials-rotate-test-key-01234")
	return crypto.NewBox(key)
}

func TestRotateEnqueuesTheRotationGroup(t *testing.T) {
	store := &rotatingStore{cred: credentials.Credential{
		ID: uuid.Must(uuid.NewV7()), OrgID: uuid.Must(uuid.NewV7()), Name: "acme", Kind: credentials.KindSSHKey,
	}}
	enq := &fakeRotationEnqueuer{count: 3}
	svc := rotationService(t, store, enq)
	actor := uuid.Must(uuid.NewV7())

	res, err := svc.Rotate(ctx, store.cred.OrgID, store.cred.ID, &actor)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if res.Count != 3 || res.GroupID == uuid.Nil || res.FirstJobID == uuid.Nil {
		t.Fatalf("result = %+v", res)
	}
	if len(enq.calls) != 1 {
		t.Fatalf("enqueue calls = %d", len(enq.calls))
	}
	call := enq.calls[0]
	if call.credentialID != store.cred.ID || call.orgID != store.cred.OrgID || call.actor == nil || *call.actor != actor {
		t.Fatalf("enqueue = %+v", call)
	}

	// The pending key is a real ed25519 key: public key, fingerprint and
	// key type are populated, the private key sealed in its Secret.
	if !strings.HasPrefix(call.key.PublicKey, "ssh-ed25519 ") ||
		!strings.HasPrefix(call.key.Fingerprint, "SHA256:") ||
		call.key.KeyType != "ed25519" {
		t.Fatalf("pending key metadata = %+v", call.key)
	}
	if call.key.PrivateKey.PrivateKey == "" || strings.Contains(call.key.PrivateKey.PrivateKey, "PRIVATE KEY") == false {
		t.Fatalf("pending private key missing")
	}
	// The credential itself is untouched on the enqueue path.
	if len(store.updates) != 0 {
		t.Fatalf("the enqueue path must not touch the credential: %+v", store.updates)
	}
}

// TestRotateGeneratesDistinctKeys pins the freshness: two rotations of the
// same credential produce two different keypairs.
func TestRotateGeneratesDistinctKeys(t *testing.T) {
	store := &rotatingStore{cred: credentials.Credential{
		ID: uuid.Must(uuid.NewV7()), OrgID: uuid.Must(uuid.NewV7()), Name: "acme", Kind: credentials.KindSSHKey,
	}}
	enq := &fakeRotationEnqueuer{count: 1}
	svc := rotationService(t, store, enq)

	if _, err := svc.Rotate(ctx, store.cred.OrgID, store.cred.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rotate(ctx, store.cred.OrgID, store.cred.ID, nil); err != nil {
		t.Fatal(err)
	}
	if len(enq.calls) != 2 {
		t.Fatalf("enqueue calls = %d", len(enq.calls))
	}
	first, second := enq.calls[0].key, enq.calls[1].key
	if first.PrivateKey.PrivateKey == second.PrivateKey.PrivateKey || first.PublicKey == second.PublicKey {
		t.Fatal("two rotations must generate two distinct keypairs")
	}
}

// TestRotateReferencedByNothingReKeysImmediately covers the count-0 path:
// the finalize runs right in Rotate, and no group is reported.
func TestRotateReferencedByNothingReKeysImmediately(t *testing.T) {
	credID, org := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	expires := time.Now().Add(24 * time.Hour)
	created := time.Now().Add(-time.Hour)
	store := &rotatingStore{cred: credentials.Credential{
		ID: credID, OrgID: org, Name: "acme", Kind: credentials.KindSSHKey, Username: "ansible",
		PublicKey: "ssh-ed25519 AAAA old", Fingerprint: "SHA256:old", KeyType: "ed25519",
		ExpiresAt: &expires, CreatedAt: created, UpdatedAt: created,
	}}
	enq := &fakeRotationEnqueuer{count: 0}
	svc := rotationService(t, store, enq)

	res, err := svc.Rotate(ctx, org, credID, nil)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if res.Count != 0 || res.GroupID != uuid.Nil || res.FirstJobID != uuid.Nil {
		t.Fatalf("result = %+v, want the immediate path", res)
	}
	// The finalize sealed the new key onto the credential.
	if len(store.updates) != 1 {
		t.Fatalf("updates = %d, want exactly one", len(store.updates))
	}
	next := store.updates[0]
	if !strings.HasPrefix(next.PublicKey, "ssh-ed25519 ") || next.PublicKey != enq.calls[0].key.PublicKey {
		t.Fatalf("public key = %q", next.PublicKey)
	}
	if next.Fingerprint != enq.calls[0].key.Fingerprint || next.KeyType != "ed25519" {
		t.Fatalf("metadata = %+v", next)
	}
	if !next.UpdatedAt.After(created) {
		t.Fatalf("updated_at = %v", next.UpdatedAt)
	}
	// The stored secret is the resealed ciphertext of the NEW private key.
	sealed := string(store.secretSeal)
	if sealed == "" || strings.Contains(sealed, enq.calls[0].key.PrivateKey.PrivateKey) {
		t.Fatalf("the stored secret must be the resealed ciphertext, got %q", sealed)
	}
	// And it opens back to the new private key under the credential's AAD:
	// the new key material is the generated OpenSSH PEM.
	plain, err := box().OpenAAD(store.secretSeal, credentials.AAD(org, credID))
	if err != nil {
		t.Fatalf("open sealed secret: %v", err)
	}
	if !strings.Contains(string(plain), "OPENSSH PRIVATE KEY") || !strings.Contains(string(plain), "BEGIN OPENSSH") {
		t.Errorf("the stored secret is not the new private key: %s", plain)
	}
}

func TestFinishRotationSealsOnlyOnSuccess(t *testing.T) {
	org, credID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store := &rotatingStore{cred: credentials.Credential{
		ID: credID, OrgID: org, Name: "acme", Kind: credentials.KindSSHKey,
		PublicKey: "ssh-ed25519 AAAA old", Fingerprint: "SHA256:old", KeyType: "ed25519",
	}}
	svc := rotationService(t, store, &fakeRotationEnqueuer{})
	pending := credentials.Secret{PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nnew\n-----END OPENSSH PRIVATE KEY-----\n"}

	// A failed group: the credential is untouched.
	if err := svc.FinishRotation(ctx, org, credID, uuid.Must(uuid.NewV7()), false, pending, "ssh-ed25519 AAAA new", "SHA256:new", "ed25519"); err != nil {
		t.Fatalf("FinishRotation(failed): %v", err)
	}
	if len(store.updates) != 0 {
		t.Fatal("a failed group must leave the credential untouched")
	}

	// A succeeded group: metadata and secret replace together.
	if err := svc.FinishRotation(ctx, org, credID, uuid.Must(uuid.NewV7()), true, pending, "ssh-ed25519 AAAA new", "SHA256:new", "ed25519"); err != nil {
		t.Fatalf("FinishRotation(success): %v", err)
	}
	if len(store.updates) != 1 {
		t.Fatalf("updates = %d", len(store.updates))
	}
	next := store.updates[0]
	if next.PublicKey != "ssh-ed25519 AAAA new" || next.Fingerprint != "SHA256:new" || next.KeyType != "ed25519" {
		t.Fatalf("metadata = %+v", next)
	}
	plain, err := box().OpenAAD(store.secretSeal, credentials.AAD(org, credID))
	if err != nil || !strings.Contains(string(plain), "OPENSSH PRIVATE KEY") || !strings.Contains(string(plain), "new") {
		t.Fatalf("the new private key must ride in the resealed secret: %q, %v", plain, err)
	}
}

func TestRotateValidationAndWiring(t *testing.T) {
	passwordCred := credentials.Credential{ID: uuid.Must(uuid.NewV7()), OrgID: uuid.Must(uuid.NewV7()), Name: "pw", Kind: credentials.KindSSHPassword}
	unknownOrg := uuid.Must(uuid.NewV7())

	// A non-ssh_key credential does not rotate.
	store := &rotatingStore{cred: passwordCred}
	svc := rotationService(t, store, &fakeRotationEnqueuer{})
	if _, err := svc.Rotate(ctx, passwordCred.OrgID, passwordCred.ID, nil); !errors.Is(err, credentials.ErrInvalidInput) {
		t.Fatalf("password credential: %v", err)
	}

	// An unknown credential is 404.
	store = &rotatingStore{cred: passwordCred, getErr: credentials.ErrNotFound}
	svc = rotationService(t, store, &fakeRotationEnqueuer{})
	if _, err := svc.Rotate(ctx, unknownOrg, passwordCred.ID, nil); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("unknown credential: %v", err)
	}

	// An unwired enqueuer fails closed.
	keyCred := credentials.Credential{ID: uuid.Must(uuid.NewV7()), OrgID: uuid.Must(uuid.NewV7()), Name: "key", Kind: credentials.KindSSHKey}
	store = &rotatingStore{cred: keyCred}
	svc = credentials.NewService(store, box(), time.Now, rand.Reader)
	if _, err := svc.Rotate(ctx, keyCred.OrgID, keyCred.ID, nil); err == nil || !strings.Contains(err.Error(), "no rotation enqueuer wired") {
		t.Fatalf("unwired enqueuer: %v", err)
	}
}

// TestRotateErrorPaths covers the failure branches the happy paths skip:
// an enqueue error propagates, a seal failure inside the finalize
// propagates and the credential store erroring on the finalize update is
// wrapped.
func TestRotateErrorPaths(t *testing.T) {
	cred := credentials.Credential{
		ID: uuid.Must(uuid.NewV7()), OrgID: uuid.Must(uuid.NewV7()), Name: "acme", Kind: credentials.KindSSHKey,
	}
	// The enqueue port failing fails Rotate.
	store := &rotatingStore{cred: cred}
	enq := &fakeRotationEnqueuer{err: errors.New("locked")}
	svc := rotationService(t, store, enq)
	if _, err := svc.Rotate(ctx, cred.OrgID, cred.ID, nil); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("enqueue error: %v", err)
	}

	// FinishRotation on an unknown credential propagates the store's
	// not-found.
	store = &rotatingStore{cred: cred, getErr: credentials.ErrNotFound}
	svc = rotationService(t, store, &fakeRotationEnqueuer{})
	if err := svc.FinishRotation(ctx, cred.OrgID, cred.ID, uuid.Must(uuid.NewV7()), true,
		credentials.Secret{PrivateKey: "pem"}, "pub", "SHA256:new", "ed25519"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("unknown credential: %v", err)
	}

	// A failing update seals nothing silently: the error names the step.
	boomStore := &failUpdateStore{Store: store}
	svc = rotationService(t, boomStore, &fakeRotationEnqueuer{})
	if err := svc.FinishRotation(ctx, cred.OrgID, cred.ID, uuid.Must(uuid.NewV7()), true,
		credentials.Secret{PrivateKey: "pem"}, "pub", "SHA256:new", "ed25519"); err == nil || !strings.Contains(err.Error(), "seal rotated credential") {
		t.Fatalf("update error: %v", err)
	}
}

// failUpdateStore is the Store whose Update always fails.
type failUpdateStore struct {
	credentials.Store
}

func (s *failUpdateStore) Get(_ context.Context, _, _ uuid.UUID) (credentials.Credential, error) {
	return credentials.Credential{ID: uuid.Must(uuid.NewV7()), OrgID: uuid.Must(uuid.NewV7()), Kind: credentials.KindSSHKey}, nil
}

func (s *failUpdateStore) Update(context.Context, credentials.Credential, []byte) (credentials.Credential, error) {
	return credentials.Credential{}, errors.New("disk full")
}
