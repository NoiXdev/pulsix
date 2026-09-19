package notify_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/notify"
)

func seedOrg(t *testing.T, d *db.DB, slug string) uuid.UUID {
	t.Helper()
	o, err := gen.New(d.Pool()).CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return o.ID
}

func TestChannelStoreRoundTripAndIsolation(t *testing.T) {
	d := testdb.Open(t)
	orgA, orgB := seedOrg(t, d, "notify-a"), seedOrg(t, d, "notify-b")
	st := notify.NewSQLStore(d)
	scopedA, scopedB := db.WithOrgScope(ctx, orgA), db.WithOrgScope(ctx, orgB)
	cfg, _ := json.Marshal(notify.EmailConfig{Recipients: []string{"ops@example.test"}})
	c, err := st.CreateChannel(scopedA, notify.Channel{ID: uuid.Must(uuid.NewV7()), OrgID: orgA, Kind: notify.KindEmail, Name: "ops", Enabled: true, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if c.Revision != 1 || c.Kind != notify.KindEmail || c.CreatedAt.IsZero() {
		t.Fatalf("created = %+v", c)
	}
	if _, err := st.CreateChannel(scopedA, notify.Channel{ID: uuid.Must(uuid.NewV7()), OrgID: orgA, Kind: notify.KindEmail, Name: "ops", Config: cfg}); !errors.Is(err, notify.ErrNameTaken) {
		t.Fatalf("duplicate name = %v", err)
	}
	// Another organization's scope sees nothing, even asking with orgA's id.
	if _, err := st.GetChannel(scopedB, orgA, c.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("cross-org get = %v", err)
	}
	if list, err := st.ListChannels(scopedB, orgA); err != nil || len(list) != 0 {
		t.Fatalf("cross-org list = %v, %v", list, err)
	}
	c.Name, c.Enabled = "ops mail", false
	up, err := st.UpdateChannel(scopedA, c, 1)
	if err != nil || up.Revision != 2 || up.Name != "ops mail" || up.Enabled {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if _, err := st.UpdateChannel(scopedA, c, 1); !errors.Is(err, notify.ErrRevisionMismatch) {
		t.Fatalf("stale update = %v", err)
	}
	c.ID = uuid.Must(uuid.NewV7())
	if _, err := st.UpdateChannel(scopedA, c, 1); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("unknown update = %v", err)
	}
	if err := st.DeleteChannel(scopedA, orgA, up.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteChannel(scopedA, orgA, up.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}
