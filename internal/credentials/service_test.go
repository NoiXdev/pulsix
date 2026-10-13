package credentials_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
)

var ctx = context.Background()

type stored struct {
	c      credentials.Credential
	sealed []byte
}

type fakeStore struct {
	rows       map[uuid.UUID]stored
	lastSealed []byte
	updates    int
	usage      map[uuid.UUID]credentials.Usage
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[uuid.UUID]stored{}} }

func (f *fakeStore) Create(_ context.Context, c credentials.Credential, sealed []byte) (credentials.Credential, error) {
	for _, r := range f.rows {
		if r.c.OrgID == c.OrgID && r.c.Name == c.Name {
			return credentials.Credential{}, credentials.ErrNameTaken
		}
	}
	f.rows[c.ID] = stored{c: c, sealed: sealed}
	f.lastSealed = sealed
	return c, nil
}

func (f *fakeStore) Get(_ context.Context, orgID, id uuid.UUID) (credentials.Credential, error) {
	r, ok := f.rows[id]
	if !ok || r.c.OrgID != orgID {
		return credentials.Credential{}, credentials.ErrNotFound
	}
	return r.c, nil
}

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID) ([]credentials.Credential, error) {
	var out []credentials.Credential
	for _, r := range f.rows {
		if r.c.OrgID == orgID {
			out = append(out, r.c)
		}
	}
	return out, nil
}

func (f *fakeStore) Update(_ context.Context, c credentials.Credential, sealed []byte) (credentials.Credential, error) {
	r, ok := f.rows[c.ID]
	if !ok || r.c.OrgID != c.OrgID {
		return credentials.Credential{}, credentials.ErrNotFound
	}
	f.updates++
	f.lastSealed = sealed
	if sealed != nil {
		r.sealed = sealed
	}
	r.c = c
	f.rows[c.ID] = r
	return c, nil
}

func (f *fakeStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	r, ok := f.rows[id]
	if !ok || r.c.OrgID != orgID {
		return credentials.ErrNotFound
	}
	delete(f.rows, id)
	return nil
}

func (f *fakeStore) Usage(_ context.Context, _ uuid.UUID) (map[uuid.UUID]credentials.Usage, error) {
	return f.usage, nil
}

func (f *fakeStore) LoadSecret(_ context.Context, orgID, id uuid.UUID) (credentials.Kind, []byte, error) {
	r, ok := f.rows[id]
	if !ok || r.c.OrgID != orgID {
		return "", nil, credentials.ErrNotFound
	}
	return r.c.Kind, r.sealed, nil
}

func testBox() *crypto.Box {
	var key [32]byte
	copy(key[:], "credentials-test-key-0123456789a")
	return crypto.NewBox(key)
}

func newService(store *fakeStore, now time.Time) *credentials.Service {
	return credentials.NewService(store, testBox(), func() time.Time { return now }, rand.Reader)
}

func openSecret(t *testing.T, c credentials.Credential, sealed []byte) credentials.Secret {
	t.Helper()
	plain, err := testBox().OpenAAD(sealed, credentials.AAD(c.OrgID, c.ID))
	if err != nil {
		t.Fatalf("open sealed secret: %v", err)
	}
	var s credentials.Secret
	if err := json.Unmarshal(plain, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateGeneratesAnED25519Key(t *testing.T) {
	store := newFakeStore()
	org := uuid.Must(uuid.NewV7())
	c, err := newService(store, time.Now()).Create(ctx, org, credentials.CreateInput{
		Name: "acme-deploy", Kind: credentials.KindSSHKey, Username: "ansible",
		Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.KeyType != "ed25519" || !strings.HasPrefix(c.Fingerprint, "SHA256:") {
		t.Fatalf("key metadata = %+v", c)
	}
	if parts := strings.Fields(c.PublicKey); len(parts) != 2 || parts[0] != "ssh-ed25519" {
		t.Fatalf("public key must be stored without a comment: %q", c.PublicKey)
	}
	secret := openSecret(t, c, store.lastSealed)
	signer, err := ssh.ParsePrivateKey([]byte(secret.PrivateKey))
	if err != nil {
		t.Fatalf("sealed private key does not parse: %v", err)
	}
	if got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))); got != c.PublicKey {
		t.Fatalf("sealed private key does not match the public key")
	}
	if _, err := testBox().OpenAAD(store.lastSealed, credentials.AAD(uuid.Must(uuid.NewV7()), c.ID)); err == nil {
		t.Fatal("the sealed secret must not open for another organization")
	}
}

func TestCreateImportsSupportedKeys(t *testing.T) {
	cases := []struct {
		name, pem, passphrase, keyType string
	}{
		{"rsa 2048", pemOf(t, rsaKey(t, 2048), ""), "", "rsa-2048"},
		{"ecdsa p256", pemOf(t, ecdsaKey(t), ""), "", "ecdsa-p256"},
		{"ed25519 with passphrase", pemOf(t, ed25519Key(t), "s3cret"), "s3cret", "ed25519"},
		{"unencrypted key with a needless passphrase", pemOf(t, ed25519Key(t), ""), "ignored", "ed25519"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newFakeStore()
			got, err := newService(store, time.Now()).Create(ctx, uuid.Must(uuid.NewV7()), credentials.CreateInput{
				Name: "imported", Kind: credentials.KindGitKey,
				Material: credentials.Material{PrivateKey: c.pem, Passphrase: c.passphrase},
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if got.KeyType != c.keyType {
				t.Fatalf("KeyType = %q, want %q", got.KeyType, c.keyType)
			}
			secret := openSecret(t, got, store.lastSealed)
			if strings.Contains(secret.PrivateKey, "ENCRYPTED") || secret.PrivateKey == c.pem && c.passphrase != "" {
				t.Fatal("an imported key must be stored decrypted, without its passphrase")
			}
			if _, err := ssh.ParsePrivateKey([]byte(secret.PrivateKey)); err != nil {
				t.Fatalf("stored key does not parse without a passphrase: %v", err)
			}
		})
	}
}

func TestCreateRejectsUnusableKeys(t *testing.T) {
	encrypted := pemOf(t, ed25519Key(t), "right")
	cases := []struct{ name, pem, passphrase, msg string }{
		{"rsa 1024", pemOf(t, rsaKey(t, 1024), ""), "", "2048"},
		{"garbage", "-----BEGIN OPENSSH PRIVATE KEY-----\nnope\n-----END OPENSSH PRIVATE KEY-----\n", "", "could not be read"},
		{"missing passphrase", encrypted, "", "passphrase"},
		{"wrong passphrase", encrypted, "wrong", "could not be read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := newService(newFakeStore(), time.Now()).Create(ctx, uuid.Must(uuid.NewV7()), credentials.CreateInput{
				Name: "bad", Kind: credentials.KindSSHKey,
				Material: credentials.Material{PrivateKey: c.pem, Passphrase: c.passphrase},
			})
			if !errors.Is(err, credentials.ErrInvalidInput) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("err = %v, want ErrInvalidInput mentioning %q", err, c.msg)
			}
		})
	}
}

func TestCreateEnforcesMaterialPerKind(t *testing.T) {
	cases := []struct {
		name string
		in   credentials.CreateInput
		msg  string
	}{
		{"key generate and import", credentials.CreateInput{Name: "k", Kind: credentials.KindSSHKey, Material: credentials.Material{Generate: true, PrivateKey: "x"}}, "exactly one"},
		{"key without material", credentials.CreateInput{Name: "k", Kind: credentials.KindSSHKey}, "exactly one"},
		{"key with password", credentials.CreateInput{Name: "k", Kind: credentials.KindSSHKey, Material: credentials.Material{Generate: true, Password: "p"}}, "key only"},
		{"password missing", credentials.CreateInput{Name: "p", Kind: credentials.KindSSHPassword}, "password is required"},
		{"password with token", credentials.CreateInput{Name: "p", Kind: credentials.KindSSHPassword, Material: credentials.Material{Password: "p", Token: "t"}}, "password only"},
		{"token missing", credentials.CreateInput{Name: "t", Kind: credentials.KindGitToken}, "token is required"},
		{"token with key", credentials.CreateInput{Name: "t", Kind: credentials.KindGitToken, Material: credentials.Material{Token: "t", Generate: true}}, "token only"},
		{"bad name", credentials.CreateInput{Name: "Bad Name", Kind: credentials.KindGitToken, Material: credentials.Material{Token: "t"}}, "name"},
		{"bad kind", credentials.CreateInput{Name: "x", Kind: "ftp", Material: credentials.Material{Token: "t"}}, "kind"},
		{"bad username", credentials.CreateInput{Name: "x", Kind: credentials.KindSSHPassword, Username: "no spaces", Material: credentials.Material{Password: "p"}}, "username"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := newService(newFakeStore(), time.Now()).Create(ctx, uuid.Must(uuid.NewV7()), c.in)
			if !errors.Is(err, credentials.ErrInvalidInput) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("err = %v, want ErrInvalidInput mentioning %q", err, c.msg)
			}
		})
	}
}

func TestCreatePasswordAndToken(t *testing.T) {
	store := newFakeStore()
	svc := newService(store, time.Now())
	org := uuid.Must(uuid.NewV7())
	pw, err := svc.Create(ctx, org, credentials.CreateInput{Name: "office-fs-admin", Kind: credentials.KindSSHPassword, Username: "Administrator", Material: credentials.Material{Password: "hunter2"}})
	if err != nil {
		t.Fatal(err)
	}
	if openSecret(t, pw, store.lastSealed).Password != "hunter2" || pw.PublicKey != "" {
		t.Fatalf("password credential = %+v", pw)
	}
	tok, err := svc.Create(ctx, org, credentials.CreateInput{Name: "gitlab-ops", Kind: credentials.KindGitToken, Username: "oauth2", Material: credentials.Material{Token: "glpat-x"}})
	if err != nil {
		t.Fatal(err)
	}
	if openSecret(t, tok, store.lastSealed).Token != "glpat-x" {
		t.Fatal("token not sealed")
	}
	if _, err := svc.Create(ctx, org, credentials.CreateInput{Name: "gitlab-ops", Kind: credentials.KindGitToken, Material: credentials.Material{Token: "x"}}); !errors.Is(err, credentials.ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
}

func TestCreateFailsWithoutEntropy(t *testing.T) {
	svc := credentials.NewService(newFakeStore(), testBox(), time.Now, iotest.ErrReader(errors.New("no entropy")))
	_, err := svc.Create(ctx, uuid.Must(uuid.NewV7()), credentials.CreateInput{Name: "k", Kind: credentials.KindSSHKey, Material: credentials.Material{Generate: true}})
	if err == nil || errors.Is(err, credentials.ErrInvalidInput) {
		t.Fatalf("err = %v, want an internal error", err)
	}
}

func TestUpdateKeepsOrReplacesTheSecret(t *testing.T) {
	store := newFakeStore()
	svc := newService(store, time.Now())
	org := uuid.Must(uuid.NewV7())
	key, err := svc.Create(ctx, org, credentials.CreateInput{Name: "k", Kind: credentials.KindSSHKey, Material: credentials.Material{Generate: true}})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := svc.Update(ctx, org, key.ID, credentials.UpdateInput{Name: "k2", Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if store.lastSealed != nil || renamed.PublicKey != key.PublicKey || renamed.Name != "k2" || renamed.Username != "deploy" {
		t.Fatalf("rename must keep the key and pass no secret: %+v sealed=%v", renamed, store.lastSealed)
	}
	rotated, err := svc.Update(ctx, org, key.ID, credentials.UpdateInput{Name: "k2", Material: credentials.Material{Generate: true}})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.PublicKey == key.PublicKey || store.lastSealed == nil {
		t.Fatal("generating again must replace the key")
	}
	tok, err := svc.Create(ctx, org, credentials.CreateInput{Name: "t", Kind: credentials.KindGitToken, Material: credentials.Material{Token: "old"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, org, tok.ID, credentials.UpdateInput{Name: "t", Material: credentials.Material{Token: "new"}}); err != nil {
		t.Fatal(err)
	}
	if openSecret(t, tok, store.lastSealed).Token != "new" {
		t.Fatal("token not replaced")
	}
	if _, err := svc.Update(ctx, org, tok.ID, credentials.UpdateInput{Name: "t", Material: credentials.Material{Password: "p"}}); !errors.Is(err, credentials.ErrInvalidInput) {
		t.Fatalf("wrong material for kind: %v", err)
	}
	if _, err := svc.Update(ctx, org, tok.ID, credentials.UpdateInput{Name: "Bad Name"}); !errors.Is(err, credentials.ErrInvalidInput) {
		t.Fatalf("bad name: %v", err)
	}
	if _, err := svc.Update(ctx, org, uuid.Must(uuid.NewV7()), credentials.UpdateInput{Name: "x"}); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("unknown credential: %v", err)
	}
}

func TestGetListDelete(t *testing.T) {
	store := newFakeStore()
	svc := newService(store, time.Now())
	org := uuid.Must(uuid.NewV7())
	c, err := svc.Create(ctx, org, credentials.CreateInput{Name: "t", Kind: credentials.KindGitToken, Material: credentials.Material{Token: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Get(ctx, org, c.ID); err != nil || got.ID != c.ID {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if list, err := svc.List(ctx, org); err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if err := svc.Delete(ctx, org, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, org, c.ID); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestExpiry(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	cases := []struct {
		name          string
		expires       *time.Time
		expired, soon bool
	}{
		{"no expiry", nil, false, false},
		{"far", at(90 * 24 * time.Hour), false, false},
		{"within warning", at(23 * 24 * time.Hour), false, true},
		{"exactly now", at(0), true, false},
		{"past", at(-time.Hour), true, false},
	}
	for _, c := range cases {
		cred := credentials.Credential{ExpiresAt: c.expires}
		if cred.Expired(now) != c.expired || cred.ExpiresSoon(now) != c.soon {
			t.Errorf("%s: expired=%v soon=%v", c.name, cred.Expired(now), cred.ExpiresSoon(now))
		}
	}
}

func TestExpiryState(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(10 * 24 * time.Hour)
	svc := newService(newFakeStore(), now)
	c := credentials.Credential{ExpiresAt: &expiresAt}
	expired, soon := svc.ExpiryState(c)
	if expired != false || soon != true {
		t.Fatalf("ExpiryState = %v, %v, want false, true", expired, soon)
	}
}

func TestAuthorizedKeyAndSnippets(t *testing.T) {
	c := credentials.Credential{Name: "acme-deploy", Kind: credentials.KindSSHKey, PublicKey: "ssh-ed25519 AAAAC3Nz"}
	want := "ssh-ed25519 AAAAC3Nz pulsix:acme:acme-deploy"
	if got := credentials.AuthorizedKey(c, "acme"); got != want {
		t.Fatalf("AuthorizedKey = %q", got)
	}
	s := credentials.SnippetsFor(c, "acme")
	for name, text := range map[string]string{"command": s.AuthorizedKeysCommand, "cloud-init": s.CloudInit, "terraform": s.Terraform} {
		if !strings.Contains(text, want) {
			t.Errorf("%s snippet misses the key: %q", name, text)
		}
	}
	if !strings.Contains(s.CloudInit, "name: "+credentials.DefaultLoginUser) {
		t.Errorf("cloud-init must default the user: %q", s.CloudInit)
	}
	c.Username = "deploy"
	if !strings.Contains(credentials.SnippetsFor(c, "acme").CloudInit, "name: deploy") {
		t.Error("cloud-init must use the credential's username")
	}
	pw := credentials.Credential{Name: "pw", Kind: credentials.KindSSHPassword}
	if credentials.AuthorizedKey(pw, "acme") != "" || credentials.SnippetsFor(pw, "acme") != (credentials.Snippets{}) {
		t.Error("a password credential has no key and no snippets")
	}
	deploy := credentials.Credential{Name: "repo", Kind: credentials.KindGitKey, PublicKey: "ssh-ed25519 AAAA"}
	if credentials.AuthorizedKey(deploy, "acme") == "" || credentials.SnippetsFor(deploy, "acme") != (credentials.Snippets{}) {
		t.Error("a deploy key shows its public key but no host snippets")
	}
}

func TestUsagePassesThrough(t *testing.T) {
	store := newFakeStore()
	id := uuid.Must(uuid.NewV7())
	store.usage = map[uuid.UUID]credentials.Usage{id: {Bastions: 1, Hosts: 3}}
	got, err := newService(store, time.Now()).Usage(ctx, uuid.Must(uuid.NewV7()))
	if err != nil || got[id].Hosts != 3 {
		t.Fatalf("Usage = %v, %v", got, err)
	}
}

func TestKindPredicates(t *testing.T) {
	if !credentials.KindGitKey.IsKey() || credentials.KindGitToken.IsKey() {
		t.Error("IsKey")
	}
	if !credentials.KindSSHPassword.IsSSH() || credentials.KindGitKey.IsSSH() {
		t.Error("IsSSH")
	}
	if credentials.Kind("nope").Valid() {
		t.Error("Valid")
	}
}

func TestOpenSecretRoundTripsAndFailsClosed(t *testing.T) {
	store := newFakeStore()
	svc := newService(store, time.Now())
	org := uuid.Must(uuid.NewV7())
	pw, err := svc.Create(ctx, org, credentials.CreateInput{Name: "host-login", Kind: credentials.KindSSHPassword, Username: "ansible", Material: credentials.Material{Password: "hunter2"}})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := svc.OpenSecret(ctx, org, pw.ID)
	if err != nil {
		t.Fatalf("OpenSecret: %v", err)
	}
	if secret.Password != "hunter2" || secret.PrivateKey != "" || secret.Token != "" {
		t.Fatalf("OpenSecret = %+v", secret)
	}
	if _, err := svc.OpenSecret(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("unknown credential: %v", err)
	}
	if _, err := svc.OpenSecret(ctx, uuid.Must(uuid.NewV7()), pw.ID); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("another org must not see the credential: %v", err)
	}
	// A ciphertext that does not open under the credential's AAD (one
	// copied from another row) must fail, never return garbage.
	store.rows[pw.ID] = stored{c: pw, sealed: []byte("tampered")}
	if _, err := svc.OpenSecret(ctx, org, pw.ID); err == nil || errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("tampered ciphertext: %v", err)
	}
}

// A vault_password holds a PASSWORD, and buildSecret's default branch —
// written when the only non-key kinds were ssh_password and git_token —
// would have demanded a token for it. The credential would then store
// Secret{Token: ...} while the worker reads Secret{Password: ...}, and
// the vault would simply never open, with nothing saying why.
func TestVaultPasswordCredentialStoresAPassword(t *testing.T) {
	ctx := context.Background()
	org := uuid.Must(uuid.NewV7())
	svc := newService(newFakeStore(), time.Now())

	c, err := svc.Create(ctx, org, credentials.CreateInput{
		Name: "prod-vault", Kind: credentials.KindVaultPassword,
		Material: credentials.Material{Password: "the-vault-passphrase"},
	})
	if err != nil {
		t.Fatalf("create vault credential: %v", err)
	}
	secret, err := svc.OpenSecret(ctx, org, c.ID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if secret.Password != "the-vault-passphrase" {
		t.Errorf("stored secret = %+v, want the passphrase in Password", secret)
	}
	if secret.Token != "" {
		t.Errorf("the passphrase was stored as a token: %+v", secret)
	}

	if _, err := svc.Create(ctx, org, credentials.CreateInput{
		Name: "wrong", Kind: credentials.KindVaultPassword,
		Material: credentials.Material{Token: "not-a-passphrase"},
	}); err == nil {
		t.Error("a vault credential given a token instead of a password must be refused")
	}
}
