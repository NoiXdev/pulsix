package workers_test

import (
	"context"
	"math/big"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/restoremark"
	"github.com/noixdev/pulsix/internal/workers"
)

var ctx = context.Background()

// fakeStore is the in-memory workers.Store the service tests run on.
type fakeStore struct {
	mu           sync.Mutex
	rows         []workers.Worker
	certs        map[string]workers.Certificate
	created      workers.Worker
	touched      []string
	enrollments  map[uuid.UUID]workers.Enrollment
	enrollHashes map[uuid.UUID][]byte
	// window is what RestoreWindow returns; adoptCalls counts
	// AdoptCertificate calls.
	window     *restoremark.Window
	adoptCalls int
}

func (f *fakeStore) init() {
	if f.certs == nil {
		f.certs = map[string]workers.Certificate{}
		f.enrollments = map[uuid.UUID]workers.Enrollment{}
		f.enrollHashes = map[uuid.UUID][]byte{}
	}
}

func (f *fakeStore) Create(_ context.Context, w workers.Worker, c workers.Certificate) (workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	return f.createLocked(w, c)
}

func (f *fakeStore) createLocked(w workers.Worker, c workers.Certificate) (workers.Worker, error) {
	for _, r := range f.rows {
		if r.OrgID == w.OrgID && r.Name == w.Name {
			return workers.Worker{}, workers.ErrNameTaken
		}
	}
	f.created = w
	f.rows = append(f.rows, w)
	f.certs[c.Serial.String()] = c
	return w, nil
}

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID) ([]workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []workers.Worker
	for _, r := range f.rows {
		if r.OrgID == orgID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) GetByName(_ context.Context, orgID uuid.UUID, name string) (workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.OrgID == orgID && r.Name == name {
			return r, nil
		}
	}
	return workers.Worker{}, workers.ErrNotFound
}

func (f *fakeStore) LookupSerial(_ context.Context, serial *big.Int) (workers.Worker, workers.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	c, ok := f.certs[serial.String()]
	if !ok {
		return workers.Worker{}, workers.Certificate{}, workers.ErrNotFound
	}
	for _, r := range f.rows {
		if r.ID == c.WorkerID {
			return r, c, nil
		}
	}
	return workers.Worker{}, workers.Certificate{}, workers.ErrNotFound
}

func (f *fakeStore) Touch(_ context.Context, id uuid.UUID, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, id.String()+":"+version)
	return nil
}

func (f *fakeStore) AddCertificate(_ context.Context, c workers.Certificate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.certs[c.Serial.String()] = c
	return nil
}

func (f *fakeStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.rows {
		if r.OrgID == orgID && r.ID == id {
			f.rows = slices.Delete(f.rows, i, i+1)
			for s, c := range f.certs {
				if c.WorkerID == id {
					delete(f.certs, s)
				}
			}
			return nil
		}
	}
	return workers.ErrNotFound
}

func (f *fakeStore) CreateEnrollment(_ context.Context, e workers.Enrollment, hash []byte) (workers.Enrollment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.enrollments[e.ID], f.enrollHashes[e.ID] = e, hash
	return e, nil
}

func (f *fakeStore) ListEnrollments(_ context.Context, orgID uuid.UUID, now time.Time) ([]workers.Enrollment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []workers.Enrollment
	for _, e := range f.enrollments {
		if e.OrgID == orgID && e.UsedAt == nil && now.Before(e.ExpiresAt) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) DeleteEnrollment(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.enrollments[id]
	if !ok || e.OrgID != orgID || e.UsedAt != nil {
		return workers.ErrNotFound
	}
	delete(f.enrollments, id)
	return nil
}

// RestoreWindow returns the window the test set.
func (f *fakeStore) RestoreWindow(context.Context) (*restoremark.Window, error) {
	return f.window, nil
}

// AdoptCertificate only counts its calls: adoption itself is covered
// against the SQL store (adopt_test.go).
func (f *fakeStore) AdoptCertificate(context.Context, string, workers.Certificate, func(workers.AdoptionState) error) (workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adoptCalls++
	return workers.Worker{}, workers.ErrNotFound
}

func (f *fakeStore) Enroll(_ context.Context, id uuid.UUID, fn func(workers.Enrollment, []byte) (workers.Worker, workers.Certificate, error)) (workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	e, ok := f.enrollments[id]
	if !ok {
		return workers.Worker{}, workers.ErrNotFound
	}
	w, c, err := fn(e, f.enrollHashes[id])
	if err != nil {
		return workers.Worker{}, err
	}
	if _, err := f.createLocked(w, c); err != nil {
		return workers.Worker{}, err
	}
	used := time.Now()
	e.UsedAt = &used
	f.enrollments[id] = e
	return w, nil
}

// Test-only state changes.
func (f *fakeStore) revokeCert(serial *big.Int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.certs[serial.String()]
	now := time.Now()
	c.RevokedAt = &now
	f.certs[serial.String()] = c
}

func (f *fakeStore) unrevokeCert(serial *big.Int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.certs[serial.String()]
	c.RevokedAt = nil
	f.certs[serial.String()] = c
}

func (f *fakeStore) setStatus(id uuid.UUID, s workers.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.rows {
		if f.rows[i].ID == id {
			f.rows[i].Status = s
		}
	}
}

func (f *fakeStore) Get(_ context.Context, orgID, id uuid.UUID) (workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.OrgID == orgID && r.ID == id {
			return r, nil
		}
	}
	return workers.Worker{}, workers.ErrNotFound
}

func (f *fakeStore) Revoke(_ context.Context, orgID, id uuid.UUID) (workers.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	for i, r := range f.rows {
		if r.OrgID == orgID && r.ID == id {
			now := time.Now()
			f.rows[i].Status, f.rows[i].RevokedAt = workers.StatusRevoked, &now
			for s, c := range f.certs {
				if c.WorkerID == id && c.RevokedAt == nil {
					c.RevokedAt = &now
					f.certs[s] = c
				}
			}
			return f.rows[i], nil
		}
	}
	return workers.Worker{}, workers.ErrNotFound
}

func (f *fakeStore) ViewDetails(context.Context, uuid.UUID) (workers.ViewDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := workers.ViewDetails{Expiry: map[uuid.UUID]time.Time{}, CurrentJob: map[uuid.UUID]uuid.UUID{}}
	for _, c := range f.certs {
		if c.RevokedAt == nil && c.NotAfter.After(d.Expiry[c.WorkerID]) {
			d.Expiry[c.WorkerID] = c.NotAfter
		}
	}
	return d, nil
}
