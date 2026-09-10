package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestWithOrgSetsLocalOrgID(t *testing.T) {
	d := testdb.Open(t)
	orgID := uuid.New()

	var setting string
	err := d.WithOrg(context.Background(), orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT current_setting('pulsix.org_id', true)`).Scan(&setting)
	})
	if err != nil {
		t.Fatal(err)
	}
	if setting != orgID.String() {
		t.Errorf("pulsix.org_id = %q, want %q", setting, orgID.String())
	}
}

func TestWithOrgSettingIsLocalToTheTransaction(t *testing.T) {
	d := testdb.Open(t)
	orgID := uuid.New()

	err := d.WithOrg(context.Background(), orgID, func(tx pgx.Tx) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	var setting string
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT current_setting('pulsix.org_id', true)`).Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if setting != "" {
		t.Errorf("pulsix.org_id leaked outside the transaction: %q", setting)
	}
}

func TestWithOrgRollsBackOnError(t *testing.T) {
	d := testdb.Open(t)
	orgID := uuid.New()
	wantErr := errors.New("boom")

	err := d.WithOrg(context.Background(), orgID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(),
			`INSERT INTO organizations (id, slug, name) VALUES ($1, $2, $3)`, orgID, orgID.String(), "rollback test"); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}

	var count int
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM organizations WHERE id = $1`, orgID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected rollback, found %d matching rows", count)
	}
}
