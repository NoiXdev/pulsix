// Package preferences stores a user's appearance settings: the list density,
// the page size of every list and the columns chosen per list (backlog
// 3.97–3.100). A user without a stored row has the defaults.
package preferences

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

// Density is how tightly list rows are packed.
type Density string

const (
	DensityComfortable Density = "comfortable"
	DensityStandard    Density = "standard"
	DensityCompact     Density = "compact"
)

const (
	defaultPageSize = 25
	maxLists        = 64
	maxColumns      = 40
	maxKnown        = 80
	maxListsBytes   = 16 * 1024
)

var (
	// ErrInvalid is wrapped with the reason a document was refused.
	ErrInvalid = errors.New("invalid preferences")
	// ErrNotFound is the store's answer for a user without a row.
	ErrNotFound = errors.New("preferences not found")

	idPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	pageSizes = map[int]bool{10: true, 25: true, 50: true, 100: true}
)

// List is the per-list choice: the columns shown, in order, and the ids of
// every column the list offered when the choice was saved (backlog 3.101), so
// a column added later can be told from one the person hid. Known is empty in
// data saved before it existed; readers then treat it as equal to Columns.
type List struct {
	Columns []string `json:"columns"`
	Known   []string `json:"known,omitempty"`
}

// Prefs is the whole preferences document of one user.
type Prefs struct {
	Density  Density
	PageSize int
	Lists    map[string]List
}

// Defaults is what a user without a stored row has.
func Defaults() Prefs {
	return Prefs{Density: DensityStandard, PageSize: defaultPageSize, Lists: map[string]List{}}
}

// Store persists one document per user.
type Store interface {
	// Get answers ErrNotFound when the user has no row.
	Get(ctx context.Context, userID uuid.UUID) (Prefs, error)
	Put(ctx context.Context, userID uuid.UUID, p Prefs) (Prefs, error)
}

// Service reads and replaces a user's preferences.
type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

// Get returns the stored document, or Defaults() when there is none.
func (s *Service) Get(ctx context.Context, userID uuid.UUID) (Prefs, error) {
	p, err := s.store.Get(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return Defaults(), nil
	}
	if err != nil {
		return Prefs{}, fmt.Errorf("get preferences: %w", err)
	}
	if p.Lists == nil {
		p.Lists = map[string]List{}
	}
	return p, nil
}

// Set validates and replaces the whole document.
func (s *Service) Set(ctx context.Context, userID uuid.UUID, p Prefs) (Prefs, error) {
	if p.Lists == nil {
		p.Lists = map[string]List{}
	}
	if err := validate(p); err != nil {
		return Prefs{}, err
	}
	out, err := s.store.Put(ctx, userID, p)
	if err != nil {
		return Prefs{}, fmt.Errorf("put preferences: %w", err)
	}
	if out.Lists == nil {
		out.Lists = map[string]List{}
	}
	return out, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func validate(p Prefs) error {
	switch p.Density {
	case DensityComfortable, DensityStandard, DensityCompact:
	default:
		return invalid("density must be comfortable, standard or compact")
	}
	if !pageSizes[p.PageSize] {
		return invalid("page size must be 10, 25, 50 or 100")
	}
	if len(p.Lists) > maxLists {
		return invalid("at most %d lists", maxLists)
	}
	for id, l := range p.Lists {
		if !idPattern.MatchString(id) {
			return invalid("list id %q is not a valid identifier", id)
		}
		if len(l.Columns) > maxColumns {
			return invalid("list %q has more than %d columns", id, maxColumns)
		}
		if _, err := checkIDs(id, "column", l.Columns); err != nil {
			return err
		}
		if len(l.Known) > maxKnown {
			return invalid("list %q has more than %d known columns", id, maxKnown)
		}
		known, err := checkIDs(id, "known column", l.Known)
		if err != nil {
			return err
		}
		if len(l.Known) > 0 {
			for _, c := range l.Columns {
				if !known[c] {
					return invalid("column %q of list %q is not among its known columns", c, id)
				}
			}
		}
	}
	raw, err := json.Marshal(p.Lists)
	if err != nil {
		return fmt.Errorf("encode lists: %w", err)
	}
	if len(raw) > maxListsBytes {
		return invalid("lists document is larger than %d bytes", maxListsBytes)
	}
	return nil
}

// checkIDs refuses ids that are malformed or repeated and returns them as a set.
func checkIDs(list, what string, ids []string) (map[string]bool, error) {
	seen := make(map[string]bool, len(ids))
	for _, c := range ids {
		if !idPattern.MatchString(c) {
			return nil, invalid("%s id %q of list %q is not a valid identifier", what, c, list)
		}
		if seen[c] {
			return nil, invalid("%s %q is repeated in list %q", what, c, list)
		}
		seen[c] = true
	}
	return seen, nil
}
