package preferences_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/preferences"
)

func TestStoreUpsertRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(t.Context(), gen.CreateUserParams{ID: uid, Email: "prefs@example.test", DisplayName: "U"}); err != nil {
		t.Fatal(err)
	}
	st := preferences.NewSQLStore(q)

	if _, err := st.Get(t.Context(), uid); !errors.Is(err, preferences.ErrNotFound) {
		t.Fatalf("Get without a row: %v, want ErrNotFound", err)
	}
	first := preferences.Prefs{Density: "compact", PageSize: 50, Lists: map[string]preferences.List{"hosts": {Columns: []string{"name"}}}}
	if _, err := st.Put(t.Context(), uid, first); err != nil {
		t.Fatal(err)
	}
	second := preferences.Prefs{Density: "comfortable", PageSize: 10, Lists: map[string]preferences.List{"agents": {Columns: []string{"name", "agent.version"}}}}
	put, err := st.Put(t.Context(), uid, second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(t.Context(), uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []preferences.Prefs{put, got} {
		if p.Density != "comfortable" || p.PageSize != 10 || len(p.Lists) != 1 ||
			len(p.Lists["agents"].Columns) != 2 || p.Lists["agents"].Columns[1] != "agent.version" {
			t.Fatalf("stored = %+v, want the second document only", p)
		}
	}

	if _, err := d.Pool().Exec(t.Context(), `DELETE FROM users WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(t.Context(), uid); !errors.Is(err, preferences.ErrNotFound) {
		t.Fatalf("Get after deleting the user: %v, want ErrNotFound", err)
	}
}
