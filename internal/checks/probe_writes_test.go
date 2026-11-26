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

// TestUpdateOfAStoredWriteWithTheSwitchOff: once the operator turns the
// switch off, a check stored while it was on can still be disabled or
// edited without touching its request (method, headers, body,
// content_type; fix round 2, N1); adding or changing a write is refused,
// also on a disabled check.
func TestUpdateOfAStoredWriteWithTheSwitchOff(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	on := checks.NewService(store, checks.WithProbeWrites(true))
	off := checks.NewService(store)
	post := httpDef("post", map[string]any{"method": "POST", "body": "x", "headers": []any{"X-A: 1"}})
	d, err := on.CreateDefinition(ctx, org, post)
	if err != nil {
		t.Fatal(err)
	}
	get, err := on.CreateDefinition(ctx, org, httpDef("get", nil))
	if err != nil {
		t.Fatal(err)
	}

	disable := post
	disable.Enabled = false
	if _, err := off.UpdateDefinition(ctx, org, d.ID, disable); err != nil {
		t.Fatalf("disabling a stored POST check: %v", err)
	}
	slower := post
	slower.Interval = 5 * time.Minute
	slower.Params = map[string]any{"url": "https://{address}/", "method": "POST", "body": "x", "headers": []string{"X-A: 1"}}
	if _, err := off.UpdateDefinition(ctx, org, d.ID, slower); err != nil {
		t.Fatalf("an update leaving the request as it is: %v", err)
	}
	for name, in := range map[string]checks.DefinitionInput{
		"another body":   httpDef("post", map[string]any{"method": "POST", "body": "y", "headers": []any{"X-A: 1"}}),
		"another method": httpDef("post", map[string]any{"method": "PUT", "body": "x", "headers": []any{"X-A: 1"}}),
		"another header": httpDef("post", map[string]any{"method": "POST", "body": "x", "headers": []any{"X-A: 2"}}),
		"a content type": httpDef("post", map[string]any{"method": "POST", "body": "x", "headers": []any{"X-A: 1"}, "content_type": "text/plain"}),
	} {
		if _, err := off.UpdateDefinition(ctx, org, d.ID, in); !errors.Is(err, checks.ErrInvalidInput) {
			t.Fatalf("%s with writes off = %v, want ErrInvalidInput", name, err)
		}
	}
	toPost := httpDef("get", map[string]any{"method": "POST"})
	toPost.Enabled = false
	if _, err := off.UpdateDefinition(ctx, org, get.ID, toPost); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("a GET check turned into a disabled POST = %v, want ErrInvalidInput", err)
	}
	// Disabling does not let a request change: enabling it later would
	// leave a write the operator never allowed.
	changedAndOff := httpDef("post", map[string]any{"method": "DELETE"})
	changedAndOff.Enabled = false
	if _, err := off.UpdateDefinition(ctx, org, d.ID, changedAndOff); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("disabling a stored write while changing it = %v, want ErrInvalidInput", err)
	}
	// Turning a stored write into a plain GET needs no switch.
	if _, err := off.UpdateDefinition(ctx, org, d.ID, httpDef("post", nil)); err != nil {
		t.Fatalf("turning a stored write into a GET: %v", err)
	}
}

func TestSameRequest(t *testing.T) {
	a := map[string]any{"url": "x", "method": "POST", "headers": []any{"X-A: 1"}, "body": "b"}
	for _, c := range []struct {
		b    map[string]any
		want bool
	}{
		{map[string]any{"url": "y", "method": "POST", "headers": []string{"X-A: 1"}, "body": "b", "timeout_s": 5}, true},
		{map[string]any{"method": "POST", "headers": []any{"X-A: 1"}}, false},
		{map[string]any{"method": "POST", "headers": []any{"X-A: 1"}, "body": "b", "content_type": "text/plain"}, false},
		{map[string]any{"headers": []any{"X-A: 1"}, "body": "b"}, false},
	} {
		if got := checks.SameRequest(a, c.b); got != c.want {
			t.Errorf("SameRequest(%v, %v) = %v, want %v", a, c.b, got, c.want)
		}
	}
}
