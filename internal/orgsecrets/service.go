// Package orgsecrets is the organization's secret store on the manager
// (backlog 3.13d, spec docs/superpowers/specs/2026-11-14-snmp-checks-design.md):
// named sets of fields the checks the Pulsix server runs itself read
// their credentials from (snmp's community or user). A secret's fields are
// sealed with the manager's box under an additional data that binds the
// ciphertext to its organization and name; no read path but Fields, the
// probe runner's, returns a value.
//
// A secret of the same name in a host's agent.yaml (internal/agent/secrets)
// is a different thing: agent-run checks read that file, never this store,
// so only server-run checks count as using a secret here.
package orgsecrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// Sentinel errors; the API maps them.
var (
	// ErrInvalidInput: a name, field name or value breaks the rules. The
	// wrapped text never carries a value.
	ErrInvalidInput = errors.New("invalid secret")
	// ErrNotFound: the organization has no secret of that name.
	ErrNotFound = errors.New("secret not found")
	// ErrConflict: a check still names the secret; the wrapped text names
	// the check keys.
	ErrConflict = errors.New("secret in use")
)

// Secret is a secret as List shows it: everything but its values.
type Secret struct {
	Name                 string
	FieldNames           []string
	CreatedAt, UpdatedAt time.Time
	// UsedBy are the keys of the server-run checks naming the secret,
	// sorted and de-duplicated (one key can be defined at several
	// bindings).
	UsedBy []string
}

// Stored is a secret's row without its sealed fields.
type Stored struct {
	Name                 string
	FieldNames           []string
	CreatedAt, UpdatedAt time.Time
}

// Store persists secrets (org_secrets).
type Store interface {
	// Upsert creates or replaces a secret whole: created_at is at on
	// create and kept on replace, updated_at is at. It returns the row as
	// stored, without its sealed fields.
	Upsert(ctx context.Context, orgID uuid.UUID, name string, sealed []byte, fieldNames []string, at time.Time) (Stored, error)
	// List returns the organization's secrets ordered by name.
	List(ctx context.Context, orgID uuid.UUID) ([]Stored, error)
	// Exists reports whether the organization has a secret of that name,
	// without reading its sealed fields.
	Exists(ctx context.Context, orgID uuid.UUID, name string) (bool, error)
	// Sealed returns a secret's sealed fields, or ErrNotFound.
	Sealed(ctx context.Context, orgID uuid.UUID, name string) ([]byte, error)
	// Delete removes a secret and reports whether there was one.
	Delete(ctx context.Context, orgID uuid.UUID, name string) (bool, error)
}

// Box seals and opens the fields (crypto.Box).
type Box interface {
	SealAAD(plain, aad []byte) ([]byte, error)
	OpenAAD(sealed, aad []byte) ([]byte, error)
}

// CheckRef is one check definition as the store's "used by" needs it: its
// key, plugin and params. Every binding counts — the organization's, a
// monitoring group's, a host's and a shared definition's projection.
type CheckRef struct {
	Key    string
	Plugin string
	Params map[string]any
}

// CheckRefs lists an organization's check definitions (the app wires it
// onto the checks service).
type CheckRefs interface {
	CheckRefs(ctx context.Context, orgID uuid.UUID) ([]CheckRef, error)
}

// Deps wires the service. Now defaults to time.Now and Plugins to
// checkplugins.Get; tests replace Plugins to have a server plugin that
// takes a secret before a real one does.
type Deps struct {
	Store   Store
	Box     Box
	Checks  CheckRefs
	Now     func() time.Time
	Plugins func(name string) (checkplugins.Descriptor, bool)
}

// Service manages an organization's secrets.
type Service struct{ d Deps }

// NewService wires the service.
func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Plugins == nil {
		d.Plugins = checkplugins.Get
	}
	return &Service{d: d}
}

// AAD is the additional data a secret's fields are sealed under: the row's
// organization and name, so a ciphertext moved to another row fails to open.
func AAD(orgID uuid.UUID, name string) []byte {
	return []byte("org_secrets|" + orgID.String() + "|" + name)
}

// Put creates or replaces the secret name with exactly fields and
// returns it as stored, with the checks using it — never a value.
func (s *Service) Put(ctx context.Context, orgID uuid.UUID, name string, fields map[string]string) (Secret, error) {
	if err := validate(name, fields); err != nil {
		return Secret{}, err
	}
	plain, err := json.Marshal(fields)
	if err != nil {
		return Secret{}, fmt.Errorf("encode secret %s: %w", name, err)
	}
	sealed, err := s.d.Box.SealAAD(plain, AAD(orgID, name))
	clear(plain)
	if err != nil {
		return Secret{}, fmt.Errorf("seal secret %s: %w", name, err)
	}
	names := slices.Sorted(maps.Keys(fields))
	row, err := s.d.Store.Upsert(ctx, orgID, name, sealed, names, s.d.Now())
	if err != nil {
		return Secret{}, fmt.Errorf("store secret %s: %w", name, err)
	}
	used, err := s.usedBy(ctx, orgID)
	if err != nil {
		return Secret{}, err
	}
	return secretOf(row, used), nil
}

// secretOf is a stored row with the checks using it.
func secretOf(r Stored, used map[string][]string) Secret {
	return Secret{Name: r.Name, FieldNames: r.FieldNames, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, UsedBy: used[r.Name]}
}

// List returns the organization's secrets with the checks using each,
// never a value.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Secret, error) {
	rows, err := s.d.Store.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	used, err := s.usedBy(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Secret, len(rows))
	for i, r := range rows {
		out[i] = secretOf(r, used)
	}
	return out, nil
}

// Names returns the names of the organization's secrets, sorted.
func (s *Service) Names(ctx context.Context, orgID uuid.UUID) ([]string, error) {
	rows, err := s.d.Store.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	return names, nil
}

// Delete removes a secret no server-run check names; ErrNotFound when
// there is none, ErrConflict naming the checks otherwise.
func (s *Service) Delete(ctx context.Context, orgID uuid.UUID, name string) error {
	if !validName(name) {
		return ErrNotFound
	}
	exists, err := s.d.Store.Exists(ctx, orgID, name)
	if err != nil {
		return fmt.Errorf("look up secret %s: %w", name, err)
	}
	if !exists {
		return ErrNotFound
	}
	used, err := s.usedBy(ctx, orgID)
	if err != nil {
		return err
	}
	if keys := used[name]; len(keys) > 0 {
		return fmt.Errorf("%w: the checks %s use it", ErrConflict, strings.Join(keys, ", "))
	}
	ok, err := s.d.Store.Delete(ctx, orgID, name)
	if err != nil {
		return fmt.Errorf("delete secret %s: %w", name, err)
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// Fields opens a secret's fields, for the probe runner only; ErrNotFound
// when there is none. A failure never carries a value.
func (s *Service) Fields(ctx context.Context, orgID uuid.UUID, name string) (map[string]string, error) {
	if !validName(name) {
		return nil, ErrNotFound
	}
	sealed, err := s.d.Store.Sealed(ctx, orgID, name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("look up secret %s: %w", name, err)
	}
	plain, err := s.d.Box.OpenAAD(sealed, AAD(orgID, name))
	if err != nil {
		return nil, fmt.Errorf("open secret %s: %w", name, err)
	}
	defer clear(plain)
	var fields map[string]string
	// The decoder's error is dropped: a syntax error quotes the input.
	if err := json.Unmarshal(plain, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("open secret %s: the stored fields are not a JSON object of strings", name)
	}
	return fields, nil
}

// usedBy maps each secret name to the sorted, de-duplicated keys of the
// server-run checks naming it. Agent-run checks are not counted: they
// read the host's agent.yaml.
func (s *Service) usedBy(ctx context.Context, orgID uuid.UUID) (map[string][]string, error) {
	refs, err := s.d.Checks.CheckRefs(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list the checks using secrets: %w", err)
	}
	used := map[string][]string{}
	for _, r := range refs {
		desc, ok := s.d.Plugins(r.Plugin)
		if !ok || desc.Runs != checkplugins.Server {
			continue
		}
		name, ok := desc.SecretNameOf(r.Params)
		if !ok || slices.Contains(used[name], r.Key) {
			continue
		}
		used[name] = append(used[name], r.Key)
	}
	for _, keys := range used {
		slices.Sort(keys)
	}
	return used, nil
}
