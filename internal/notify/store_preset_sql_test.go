package notify_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/notify"
)

// TestSQLStoreKeepsPreset: a webhook channel's preset (backlog 4.7) survives
// create, get, list and update, and the dispatcher's claim carries it — the
// sender picks the renderer from it.
func TestSQLStoreKeepsPreset(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "notify-preset")
	st := notify.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	c, err := st.CreateChannel(scoped, notify.Channel{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: notify.KindWebhook, Preset: "slack", Name: "chat", Enabled: true,
		Config: json.RawMessage(`{"method":"POST"}`), BodyJSON: true,
	})
	if err != nil || c.Preset != "slack" {
		t.Fatalf("create = %+v, %v", c, err)
	}
	if got, err := st.GetChannel(scoped, org, c.ID); err != nil || got.Preset != "slack" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if list, err := st.ListChannels(scoped, org); err != nil || len(list) != 1 || list[0].Preset != "slack" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	c.Preset = "telegram"
	up, err := st.UpdateChannel(scoped, c, c.Revision)
	if err != nil || up.Preset != "telegram" {
		t.Fatalf("update = %+v, %v", up, err)
	}
	now := time.Now()
	queueDelivery(t, d, up, "test:"+uuid.NewString(), now.Add(-time.Minute))
	claimed, err := notify.NewDeliverySQLStore(d).Claim(ctx, time.Minute, 10)
	if err != nil || len(claimed) != 1 || claimed[0].Channel.Preset != "telegram" {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
}

// TestSQLStorePresetChecks: the schema refuses a preset it does not know and
// a preset on an e-mail channel.
func TestSQLStorePresetChecks(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "notify-preset-check")
	st := notify.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	for _, c := range []notify.Channel{
		{Kind: notify.KindWebhook, Preset: "discord", Config: json.RawMessage(`{}`)},
		{Kind: notify.KindEmail, Preset: "slack", Config: json.RawMessage(`{"recipients":["a@example.test"]}`)},
	} {
		c.ID, c.OrgID, c.Name = uuid.Must(uuid.NewV7()), org, "c "+uuid.NewString()[:6]
		if _, err := st.CreateChannel(scoped, c); err == nil {
			t.Fatalf("%s/%s stored", c.Kind, c.Preset)
		}
	}
}
