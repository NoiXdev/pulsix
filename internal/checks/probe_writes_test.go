package checks_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

func httpDef(key string, params map[string]any) checks.DefinitionInput {
	p := map[string]any{"url": "https://{address}/"}
	for k, v := range params {
		p[k] = v
	}
	return checks.DefinitionInput{Plugin: "http", Key: key, Interval: time.Minute, BindingKind: checks.BindingOrg, Enabled: true, Params: p}
}

// TestDefinitionsRefuseWritesUnlessAllowed: an http check that sends
// POST, PUT, PATCH, DELETE or a body is accepted only by a service the
// operator allowed it (PULSIX_PROBE_WRITE_METHODS, backlog 3.84 fix
// round 1); GET, HEAD and OPTIONS with headers always are.
func TestDefinitionsRefuseWritesUnlessAllowed(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	off := checks.NewService(newFakeStore())
	on := checks.NewService(newFakeStore(), checks.WithProbeWrites(true))
	reads := map[string]map[string]any{
		"get":     {"headers": []any{"X-A: 1"}},
		"head":    {"method": "HEAD"},
		"options": {"method": "OPTIONS", "headers": []any{"X-A: 1"}},
	}
	writes := map[string]map[string]any{
		"post":   {"method": "POST"},
		"put":    {"method": "PUT", "body": "x"},
		"patch":  {"method": "PATCH", "body": "x"},
		"delete": {"method": "DELETE"},
	}
	for key, params := range reads {
		if _, err := off.CreateDefinition(ctx, org, httpDef(key, params)); err != nil {
			t.Fatalf("%s with writes off: %v", key, err)
		}
	}
	for key, params := range writes {
		_, err := off.CreateDefinition(ctx, org, httpDef(key, params))
		if !errors.Is(err, checks.ErrInvalidInput) || !strings.Contains(err.Error(), "PULSIX_PROBE_WRITE_METHODS") {
			t.Fatalf("%s with writes off = %v, want a refusal naming the setting", key, err)
		}
		if _, err := on.CreateDefinition(ctx, org, httpDef(key, params)); err != nil {
			t.Fatalf("%s with writes on: %v", key, err)
		}
	}
	// An update to a write is refused the same way.
	d, err := off.CreateDefinition(ctx, org, httpDef("later", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := off.UpdateDefinition(ctx, org, d.ID, httpDef("later", map[string]any{"method": "DELETE"})); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("update to DELETE with writes off = %v, want ErrInvalidInput", err)
	}
}

func TestRefuseWrites(t *testing.T) {
	if err := checks.RefuseWrites("http", map[string]any{"method": "POST"}, false); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("POST off = %v", err)
	}
	if err := checks.RefuseWrites("http", map[string]any{"method": "POST"}, true); err != nil {
		t.Fatalf("POST on = %v", err)
	}
	if err := checks.RefuseWrites("tcp", map[string]any{"port": 22}, false); err != nil {
		t.Fatalf("tcp = %v", err)
	}
	if err := checks.RefuseWrites("nope", nil, false); err != nil {
		t.Fatalf("unknown plugin = %v", err)
	}
}
