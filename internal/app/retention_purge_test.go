package app

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// TestBuildRunsTheRetentionPurge (backlog 3.15): Build starts the
// per-organization retention purge with the instance maximum: an
// organization keeping 30 days loses its 40-day-old sample, keeps its
// 10-day-old one, and an organization without a choice keeps both.
func TestBuildRunsTheRetentionPurge(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)

	admin, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	q := gen.New(admin.Pool())
	now := time.Now().UTC()
	seed := func(slug string) (org, host uuid.UUID) {
		o, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
		if err != nil {
			t.Fatal(err)
		}
		zone, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: o.ID, Slug: slug, Name: slug})
		if err != nil {
			t.Fatal(err)
		}
		h, err := q.CreateHost(ctx, gen.CreateHostParams{ID: uuid.Must(uuid.NewV7()), OrgID: o.ID, ZoneID: zone.ID, Name: slug, Facts: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		for _, days := range []int{40, 10} {
			if _, err := admin.Pool().Exec(ctx, `
				INSERT INTO check_samples (at, org_id, host_id, check_key, item, metric, value)
				VALUES ($1, $2, $3, 'cpu', '', 'usage', 1)`, now.AddDate(0, 0, -days), o.ID, h.ID); err != nil {
				t.Fatal(err)
			}
		}
		return o.ID, h.ID
	}
	short, _ := seed("purge-app-short")
	keep, _ := seed("purge-app-keep")
	if _, err := admin.Pool().Exec(ctx, `INSERT INTO org_monitoring_settings (org_id, retention_days) VALUES ($1, 30)`, short); err != nil {
		t.Fatal(err)
	}
	samples := func(org uuid.UUID) (older, newer int) {
		if err := admin.Pool().QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE at < $2), count(*) FILTER (WHERE at >= $2)
			FROM check_samples WHERE org_id = $1`, org, now.AddDate(0, 0, -30)).Scan(&older, &newer); err != nil {
			t.Fatal(err)
		}
		return older, newer
	}

	a, err := Build(ctx, cfg, WithRetentionPurgeInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if older, _ := samples(short); older == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the retention purge never deleted the 30-day organization's older sample")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, newer := samples(short); newer != 1 {
		t.Fatalf("30-day organization's newer samples = %d, want 1", newer)
	}
	if older, newer := samples(keep); older != 1 || newer != 1 {
		t.Fatalf("organization without a choice = %d older, %d newer; want both kept", older, newer)
	}
}
