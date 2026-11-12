package secrets

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSetNeverPrintsAValue(t *testing.T) {
	set, err := parse([]byte("secrets:\n  pg:\n    password: " + secretValue + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if got := fmt.Sprintf(format, set); strings.Contains(got, secretValue) || !strings.Contains(got, "pg") {
			t.Errorf("Sprintf(%s) = %q, want the names only", format, got)
		}
		if got := fmt.Sprintf(format, &set); strings.Contains(got, secretValue) {
			t.Errorf("Sprintf(%s, pointer) = %q", format, got)
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("loaded", "secrets", set)
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("loaded", "secrets", set)
	if got := buf.String(); strings.Contains(got, secretValue) || !strings.Contains(got, "pg") {
		t.Errorf("log = %q, want the names only", got)
	}
}

func TestSetFieldsIsACopy(t *testing.T) {
	set, err := parse([]byte("secrets:\n  pg:\n    password: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	f, _ := set.Fields("pg")
	f["password"] = "changed"
	f["new"] = "y"
	if again, _ := set.Fields("pg"); again["password"] != "x" || len(again) != 1 {
		t.Fatalf("Fields shares its map: %v", again)
	}
	names := set.Names()
	names[0] = "changed"
	if set.Names()[0] != "pg" {
		t.Fatal("Names shares its slice")
	}
}

func TestZeroSetIsEmpty(t *testing.T) {
	var set Set
	if set.Len() != 0 || len(set.Names()) != 0 {
		t.Fatalf("zero Set = %v", set)
	}
	if _, ok := set.Fields("x"); ok {
		t.Fatal("zero Set has a secret")
	}
}
