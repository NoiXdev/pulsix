package api_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
)

func idsQuery(ids ...uuid.UUID) url.Values {
	v := url.Values{}
	for _, id := range ids {
		v.Add("id", id.String())
	}
	return v
}

func TestHostsListByIDsReturnsExactlyThose(t *testing.T) {
	f := newHostPageFixture(t, "hostids-basic", 12)
	got := f.get(t, idsQuery(f.hostIDs[3], f.hostIDs[1]).Encode())
	if want := []string{"h-02", "h-04"}; !slices.Equal(hostNames(got.Hosts), want) {
		t.Fatalf("hosts = %v, want %v", hostNames(got.Hosts), want)
	}
}

func TestHostsListByIDsHidesForeignAndUnknownIDs(t *testing.T) {
	f := newHostPageFixture(t, "hostids-foreign", 4)
	owner := f.s.SeedUser("hostids-other@example.test", "correct horse battery")
	other := f.s.SeedOrg("hostids-other", owner)
	scoped := db.WithOrgScope(context.Background(), other)
	zs, err := f.s.Deps.Hosts.ListZones(scoped, other)
	if err != nil || len(zs) == 0 {
		t.Fatalf("zones: %v", err)
	}
	foreign, err := f.s.Deps.Hosts.CreateHost(scoped, other, hosts.HostInput{Name: "alien", Address: "10.9.9.9", ZoneID: zs[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	q := idsQuery(f.hostIDs[0], foreign.ID, uuid.New())
	got := f.get(t, q.Encode())
	if want := []string{"h-01"}; !slices.Equal(hostNames(got.Hosts), want) {
		t.Fatalf("hosts = %v, want %v", hostNames(got.Hosts), want)
	}
}

func TestHostsListByIDsCombinesWithOtherFilters(t *testing.T) {
	f := newHostPageFixture(t, "hostids-combo", 8)
	// Hosts h-02, h-04, ... are managed; h-01 and h-02 are asked for.
	q := idsQuery(f.hostIDs[0], f.hostIDs[1])
	q.Set("managed", "true")
	got := f.get(t, q.Encode())
	if want := []string{"h-02"}; !slices.Equal(hostNames(got.Hosts), want) {
		t.Fatalf("hosts = %v, want %v", hostNames(got.Hosts), want)
	}
	q.Set("page", "1")
	got = f.get(t, q.Encode())
	if got.Total == nil || *got.Total != 1 || !slices.Equal(hostNames(got.Hosts), []string{"h-02"}) {
		t.Fatalf("page mode = %v total %v, want h-02 / 1", hostNames(got.Hosts), got.Total)
	}
}

func TestHostsListByIDsAnswersFivehundredIDsInOnePage(t *testing.T) {
	f := newHostPageFixture(t, "hostids-limit", 3)
	ids := make([]uuid.UUID, 0, 500)
	ids = append(ids, f.hostIDs...)
	for len(ids) < 500 {
		ids = append(ids, uuid.New())
	}
	q := idsQuery(ids...)
	q.Set("limit", "1") // ignored: named hosts come in one page
	got := f.get(t, q.Encode())
	if len(got.Hosts) != 3 {
		t.Fatalf("hosts = %v, want the 3 real ones", hostNames(got.Hosts))
	}
}

func TestHostsListByIDsRefusesMoreThanFivehundred(t *testing.T) {
	f := newHostPageFixture(t, "hostids-over", 1)
	ids := make([]uuid.UUID, 501)
	for i := range ids {
		ids[i] = uuid.New()
	}
	if got := f.status(t, idsQuery(ids...).Encode()); got != http.StatusUnprocessableEntity {
		t.Fatalf("501 ids: status %d, want 422", got)
	}
}

func TestHostsListByIDsRejectsMalformedID(t *testing.T) {
	f := newHostPageFixture(t, "hostids-bad", 1)
	if got := f.status(t, "id=not-a-uuid"); got != http.StatusBadRequest {
		t.Fatalf("bad id: status %d, want 400", got)
	}
}
