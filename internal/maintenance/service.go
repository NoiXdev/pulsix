package maintenance

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// maxOccurrencesPerWindow bounds one window's occurrences in a range (an
// every-minute recurring window over six weeks would be 60 000).
const maxOccurrencesPerWindow = 500

// Store persists windows (org-scoped).
type Store interface {
	Create(ctx context.Context, w Window) (Window, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Window, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Window, error)
	Update(ctx context.Context, w Window, revision int64) (Window, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
}

// GroupHosts resolves an inventory group (and its subgroups) into the
// managed hosts in it; the app wires the job scope resolver behind it.
type GroupHosts interface {
	HostsInGroup(ctx context.Context, orgID, groupID uuid.UUID) ([]uuid.UUID, error)
}

// Service manages windows and answers which hosts are in maintenance.
type Service struct {
	store  Store
	groups GroupHosts
	now    func() time.Time
	rand   io.Reader
	log    *slog.Logger
}

// NewService wires the service; nil now, rand and log take defaults.
func NewService(store Store, groups GroupHosts, now func() time.Time, entropy io.Reader, log *slog.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, groups: groups, now: now, rand: entropy, log: log}
}

func (s *Service) newID() uuid.UUID {
	id, err := uuid.NewV7FromReader(s.rand)
	if err != nil {
		panic(err) //nolint:forbidigo // entropy failure is unrecoverable, like uuid.Must
	}
	return id
}

// Create validates and stores a window.
func (s *Service) Create(ctx context.Context, orgID, actor uuid.UUID, in Input) (Window, error) {
	w, err := normalize(in, s.now())
	if err != nil {
		return Window{}, err
	}
	w.ID, w.OrgID, w.CreatedBy = s.newID(), orgID, &actor
	return s.store.Create(ctx, w)
}

// Update replaces a window when revision is still current.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, revision int64, in Input) (Window, error) {
	w, err := normalize(in, s.now())
	if err != nil {
		return Window{}, err
	}
	w.ID, w.OrgID = id, orgID
	return s.store.Update(ctx, w, revision)
}

func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Window, error) {
	return s.store.Get(ctx, orgID, id)
}

// List returns the organization's windows and which of them are active
// right now.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Window, map[uuid.UUID]bool, error) {
	windows, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	active := map[uuid.UUID]bool{}
	for _, w := range windows {
		if ok, err := ActiveAt(w, now); err == nil && ok {
			active[w.ID] = true
		}
	}
	return windows, active, nil
}

func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.Delete(ctx, orgID, id)
}

// HostsInMaintenance returns the subset of hostIDs that an active window
// covers at the instant at: an organization window covers every host, a
// group window the managed hosts of the group and its subgroups. A stored
// window that no longer evaluates is logged and ignored.
func (s *Service) HostsInMaintenance(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID, at time.Time) (map[uuid.UUID]bool, error) {
	windows, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	covered := map[uuid.UUID]bool{}
	all := false
	for _, w := range windows {
		active, err := ActiveAt(w, at)
		if err != nil {
			s.log.ErrorContext(ctx, "maintenance window does not evaluate; ignoring it", "window", w.ID, "org", orgID, "err", err)
			continue
		}
		if !active {
			continue
		}
		switch w.TargetKind {
		case TargetOrg:
			all = true
		case TargetHost:
			covered[*w.TargetID] = true
		case TargetGroup:
			members, err := s.groups.HostsInGroup(ctx, orgID, *w.TargetID)
			if err != nil {
				return nil, fmt.Errorf("resolve maintenance group %s: %w", w.TargetID, err)
			}
			for _, h := range members {
				covered[h] = true
			}
		}
	}
	out := map[uuid.UUID]bool{}
	for _, h := range hostIDs {
		if all || covered[h] {
			out[h] = true
		}
	}
	return out, nil
}

// OccurrencesBetween lists every window's stretches overlapping
// [from, to), ordered by start.
func (s *Service) OccurrencesBetween(ctx context.Context, orgID uuid.UUID, from, to time.Time) ([]Occurrence, error) {
	windows, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var out []Occurrence
	for _, w := range windows {
		occ, err := Occurrences(w, from, to, maxOccurrencesPerWindow)
		if err != nil {
			s.log.ErrorContext(ctx, "maintenance window does not evaluate; leaving it out of the calendar", "window", w.ID, "org", orgID, "err", err)
			continue
		}
		out = append(out, occ...)
	}
	sortOccurrences(out)
	return out, nil
}
