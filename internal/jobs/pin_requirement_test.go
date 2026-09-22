// The org-level pin requirement (backlog 2.44): when an organization sets
// jobs_require_pinned_hosts, every enqueue path that drives SSH — template
// runs, ad-hoc commands, bootstraps and rotation targets — refuses hosts
// without a FULL pin: no pin at all, or a fingerprint-only pin whose key
// the worker's known_hosts could not enforce anyway. The refusal names the
// affected hosts; the connection test is how a host gets pinned and is
// never refused. The unit tests drive fakes; the store's SQL behavior and
// the HTTP routes are covered by the store and apitest suites.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// settingsStore is a rotationOnlyStore (the full Store stand-in) plus the
// jobSettingsStore capability, backed by a per-org require map. A missing
// map entry models an organization without a stored row:
// GetOrgJobSettings answers the default (false).
type settingsStore struct {
	*rotationOnlyStore
	mu      sync.Mutex
	require map[uuid.UUID]bool
}

func (s *settingsStore) GetOrgJobSettings(_ context.Context, orgID uuid.UUID) (OrgJobSettings, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	require, ok := s.require[orgID]
	return OrgJobSettings{JobsRequirePinnedHosts: require}, ok, nil
}

func (s *settingsStore) PutOrgJobSettings(_ context.Context, orgID uuid.UUID, st OrgJobSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.require == nil {
		s.require = map[uuid.UUID]bool{}
	}
	s.require[orgID] = st.JobsRequirePinnedHosts
	return nil
}

// fullPinHosts is the HostLookup + KeyGuardSource + FullPinSource fake:
// which hosts exist and which of them carry a FULL pin (the full public
// key). A host absent from known is unpinned or fingerprint-only —
// refused either way.
type fullPinHosts struct {
	byID  map[uuid.UUID]hosts.Host
	known map[uuid.UUID]bool
}

func (f fullPinHosts) GetHost(_ context.Context, _, id uuid.UUID) (hosts.Host, error) {
	h, ok := f.byID[id]
	if !ok {
		return hosts.Host{}, hosts.ErrNotFound
	}
	return h, nil
}

func (f fullPinHosts) ChangedHosts(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error) {
	return map[uuid.UUID]bool{}, nil
}

func (f fullPinHosts) PinnedPublicKeys(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	for _, id := range ids {
		if f.known[id] {
			out[id] = pinPub
		}
	}
	return out, nil
}

// pinOrgFixture seeds one org, a default zone and three managed hosts:
// fully pinned, fingerprint-only pinned and bare (no pin at all).
type pinOrgFixture struct {
	org, zone                   uuid.UUID
	pinned, fingerprinted, bare uuid.UUID
	store                       *settingsStore
	svc                         *Service
}

func seedPinOrg(t *testing.T) pinOrgFixture {
	t.Helper()
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	pinned, fingerprinted, bare := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hs := fullPinHosts{
		byID: map[uuid.UUID]hosts.Host{
			pinned:        {ID: pinned, OrgID: org, ZoneID: zone, Managed: true},
			fingerprinted: {ID: fingerprinted, OrgID: org, ZoneID: zone, Managed: true},
			bare:          {ID: bare, OrgID: org, ZoneID: zone, Managed: true},
		},
		known: map[uuid.UUID]bool{pinned: true},
	}
	store := &settingsStore{rotationOnlyStore: newRotationOnlyStore()}
	svc := NewService(store, noQueue{}, fakeTemplates{}, nil, hs, oneZone{zone}, fakeBox{}, time.Now, rand.Reader)
	return pinOrgFixture{org: org, zone: zone, pinned: pinned, fingerprinted: fingerprinted, bare: bare, store: store, svc: svc}
}

func TestOrgJobSettingsDefaultIsOffAndRoundTrips(t *testing.T) {
	f := seedPinOrg(t)
	ctx := db.WithOrgScope(context.Background(), f.org)

	st, err := f.svc.OrgJobSettings(ctx, f.org)
	if err != nil || st.JobsRequirePinnedHosts {
		t.Fatalf("OrgJobSettings before any write = %+v, %v; want the default false", st, err)
	}
	if err := f.svc.SetOrgJobSettings(ctx, f.org, OrgJobSettings{JobsRequirePinnedHosts: true}); err != nil {
		t.Fatalf("SetOrgJobSettings: %v", err)
	}
	st, err = f.svc.OrgJobSettings(ctx, f.org)
	if err != nil || !st.JobsRequirePinnedHosts {
		t.Fatalf("OrgJobSettings = %+v, %v; want true", st, err)
	}
	if err := f.svc.SetOrgJobSettings(ctx, f.org, OrgJobSettings{}); err != nil {
		t.Fatal(err)
	}
	if st, err = f.svc.OrgJobSettings(ctx, f.org); err != nil || st.JobsRequirePinnedHosts {
		t.Fatalf("OrgJobSettings = %+v, %v; want false again", st, err)
	}
}

func TestOrgJobSettingsIsScopedPerOrg(t *testing.T) {
	f := seedPinOrg(t)
	other := uuid.Must(uuid.NewV7())
	if err := f.svc.SetOrgJobSettings(db.WithOrgScope(context.Background(), f.org), f.org, OrgJobSettings{JobsRequirePinnedHosts: true}); err != nil {
		t.Fatal(err)
	}
	st, err := f.svc.OrgJobSettings(db.WithOrgScope(context.Background(), other), other)
	if err != nil || st.JobsRequirePinnedHosts {
		t.Fatalf("other org's settings = %+v, %v; want the default false", st, err)
	}
}

func TestEnqueuesRefuseUnpinnedHostsWhenTheOrgRequiresPins(t *testing.T) {
	cases := []struct {
		name    string
		refused bool
		run     func(f pinOrgFixture, ids []uuid.UUID) error
	}{
		{"ad-hoc", true, func(f pinOrgFixture, ids []uuid.UUID) error {
			_, err := f.svc.EnqueueAdhoc(db.WithOrgScope(context.Background(), f.org), f.org, ids, "shell", "true", nil)
			return err
		}},
		{"bootstrap", true, func(f pinOrgFixture, ids []uuid.UUID) error {
			_, err := f.svc.EnqueueHostJob(db.WithOrgScope(context.Background(), f.org), f.org, KindBootstrap, ids, json.RawMessage(`{}`), TriggerManual, nil)
			return err
		}},
		{"rotation", true, func(f pinOrgFixture, ids []uuid.UUID) error {
			targets := make([]RotationHost, len(ids))
			for i, id := range ids {
				targets[i] = RotationHost{ID: id, ZoneID: f.zone}
			}
			f.svc.rotationTargets = fakeRotationTargets{hosts: targets, bastions: []sshconf.Bastion{}}
			_, _, _, err := f.svc.EnqueueRotation(db.WithOrgScope(context.Background(), f.org), f.org, uuid.Must(uuid.NewV7()), credentials.PendingKey{PublicKey: "ssh-ed25519 AAAA new"}, nil)
			return err
		}},
		{"template run", true, func(f pinOrgFixture, ids []uuid.UUID) error {
			templateID := uuid.Must(uuid.NewV7())
			f.svc.templates = fakeTemplates{byID: map[uuid.UUID]TemplateRow{
				templateID: {ID: templateID, OrgID: f.org, PlaybookInline: "- hosts: all", Scope: json.RawMessage(`{}`)},
			}}
			f.svc.resolver = pinRunResolver{targets: []ZoneTargets{{ZoneID: f.zone, HostIDs: ids}}}
			_, err := f.svc.EnqueueRun(db.WithOrgScope(context.Background(), f.org), f.org, templateID, nil)
			return err
		}},
		// The connection test is how a host gets pinned in the first
		// place: it is never refused.
		{"connection test", false, func(f pinOrgFixture, ids []uuid.UUID) error {
			_, err := f.svc.EnqueueHostJob(db.WithOrgScope(context.Background(), f.org), f.org, KindConnectionTest, ids, nil, TriggerManual, nil)
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name+" refuses unpinned targets", func(t *testing.T) {
			f := seedPinOrg(t)
			if err := f.svc.SetOrgJobSettings(db.WithOrgScope(context.Background(), f.org), f.org, OrgJobSettings{JobsRequirePinnedHosts: true}); err != nil {
				t.Fatal(err)
			}
			err := c.run(f, []uuid.UUID{f.pinned, f.fingerprinted, f.bare})
			if c.refused {
				if !errors.Is(err, ErrUnpinnedHosts) {
					t.Fatalf("enqueue = %v, want ErrUnpinnedHosts", err)
				}
				for _, id := range []uuid.UUID{f.fingerprinted, f.bare} {
					if !strings.Contains(err.Error(), id.String()) {
						t.Fatalf("error %q must name host %s", err, id)
					}
				}
				if strings.Contains(err.Error(), f.pinned.String()) {
					t.Fatalf("error %q must not name the pinned host %s", err, f.pinned)
				}
			} else if err != nil {
				t.Fatalf("enqueue = %v, want no refusal", err)
			}
		})
		t.Run(c.name+" passes with the setting off", func(t *testing.T) {
			f := seedPinOrg(t)
			if err := c.run(f, []uuid.UUID{f.fingerprinted, f.bare}); err != nil {
				t.Fatalf("enqueue = %v, want no refusal while the setting is off", err)
			}
		})
	}
}

// TestRunRefusalHonorsExcludedHosts: a run whose request explicitly
// excludes its unpinned host enqueues normally — exclusion runs before the
// refusal, so an operator can exclude instead of pin.
func TestRunRefusalHonorsExcludedHosts(t *testing.T) {
	f := seedPinOrg(t)
	if err := f.svc.SetOrgJobSettings(db.WithOrgScope(context.Background(), f.org), f.org, OrgJobSettings{JobsRequirePinnedHosts: true}); err != nil {
		t.Fatal(err)
	}
	templateID := uuid.Must(uuid.NewV7())
	f.svc.templates = fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, OrgID: f.org, PlaybookInline: "- hosts: all", Scope: json.RawMessage(`{}`)},
	}}
	f.svc.resolver = pinRunResolver{targets: []ZoneTargets{{ZoneID: f.zone, HostIDs: []uuid.UUID{f.pinned, f.bare}}}}
	created, err := f.svc.EnqueueRunWith(db.WithOrgScope(context.Background(), f.org), f.org, templateID,
		RunRequest{Trigger: TriggerManual, ExcludeHosts: []uuid.UUID{f.bare}})
	if err != nil {
		t.Fatalf("EnqueueRunWith excluding the unpinned host: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("jobs = %d, want one", len(created))
	}
}

// TestRefusalFailsClosedOnSettingReadError: a settings read that fails
// must not let the enqueue through — an unreadable requirement is treated
// like an unsatisfied one for the refusing paths, never as silently off.
func TestRefusalFailsClosedOnSettingReadError(t *testing.T) {
	f := seedPinOrg(t)
	errBoom := errors.New("settings exploded")
	f.svc.settings = failingSettings{err: errBoom}
	if _, err := f.svc.EnqueueAdhoc(db.WithOrgScope(context.Background(), f.org), f.org, []uuid.UUID{f.pinned}, "shell", "true", nil); !errors.Is(err, errBoom) {
		t.Fatalf("EnqueueAdhoc = %v, want the settings failure", err)
	}
}

type failingSettings struct{ err error }

func (f failingSettings) GetOrgJobSettings(context.Context, uuid.UUID) (OrgJobSettings, bool, error) {
	return OrgJobSettings{}, false, f.err
}

func (failingSettings) PutOrgJobSettings(context.Context, uuid.UUID, OrgJobSettings) error {
	return errors.New("not wired")
}

// pinRunResolver is the ScopeResolver stand-in the run cases need.
type pinRunResolver struct{ targets []ZoneTargets }

func (r pinRunResolver) Resolve(context.Context, uuid.UUID, render.Scope, string) ([]ZoneTargets, error) {
	return r.targets, nil
}
