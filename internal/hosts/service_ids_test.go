package hosts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/paging"
)

// idsRecorder notes what the service hands the store, for the id filter's
// two rules (a cap, and a page size of its own).
type idsRecorder struct {
	*fakeHostStore
	listCalls, pageCalls int
	listLimit            int
}

func (r *idsRecorder) ListHosts(_ context.Context, _ uuid.UUID, _ hosts.ListFilter, _ string, limit int) ([]hosts.Host, string, error) {
	r.listCalls++
	r.listLimit = limit
	return nil, "", nil
}

func (r *idsRecorder) ListHostsPage(context.Context, uuid.UUID, hosts.ListFilter, paging.Page) ([]hosts.Host, int, error) {
	r.pageCalls++
	return nil, 0, nil
}

func idList(n int) []uuid.UUID {
	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = uuid.Must(uuid.NewV7())
	}
	return out
}

func TestListHostsIDFilter(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	tests := []struct {
		name      string
		ids       int
		limit     int
		wantErr   bool
		wantLimit int // the limit the store sees, when the call goes through
	}{
		{"no ids use the clamped limit", 0, 10, false, 10},
		{"no ids and a huge limit clamp", 0, 100000, false, hosts.MaxListLimit},
		{"some ids ignore the limit and get the id page size", 3, 10, false, hosts.MaxListIDs},
		{"exactly the cap is allowed", hosts.MaxListIDs, 1, false, hosts.MaxListIDs},
		{"one over the cap is invalid input", hosts.MaxListIDs + 1, 10, true, 0},
	}
	for _, tc := range tests {
		t.Run("ListHosts/"+tc.name, func(t *testing.T) {
			r := &idsRecorder{fakeHostStore: newFakeHostStore()}
			svc := hosts.NewService(r, time.Now)
			_, _, err := svc.ListHosts(ctx, org, hosts.ListFilter{IDs: idList(tc.ids)}, "", tc.limit)
			if tc.wantErr {
				if !errors.Is(err, hosts.ErrInvalidInput) {
					t.Fatalf("err = %v, want ErrInvalidInput", err)
				}
				if r.listCalls != 0 {
					t.Fatal("the store was reached despite the cap")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.listCalls != 1 || r.listLimit != tc.wantLimit {
				t.Fatalf("store calls = %d, limit = %d, want 1 call with %d", r.listCalls, r.listLimit, tc.wantLimit)
			}
		})
		t.Run("ListHostsPage/"+tc.name, func(t *testing.T) {
			if tc.ids == 0 {
				t.Skip("the cap is the only id rule of the offset page")
			}
			r := &idsRecorder{fakeHostStore: newFakeHostStore()}
			svc := hosts.NewService(r, time.Now)
			_, _, err := svc.ListHostsPage(ctx, org, hosts.ListFilter{IDs: idList(tc.ids)}, paging.Page{})
			if tc.wantErr {
				if !errors.Is(err, hosts.ErrInvalidInput) {
					t.Fatalf("err = %v, want ErrInvalidInput", err)
				}
				if r.pageCalls != 0 {
					t.Fatal("the store was reached despite the cap")
				}
				return
			}
			if err != nil || r.pageCalls != 1 {
				t.Fatalf("err = %v, store calls = %d, want nil and 1", err, r.pageCalls)
			}
		})
	}
}
