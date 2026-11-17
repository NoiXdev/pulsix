package mcp

import (
	"encoding/json"
	"testing"
)

func TestWithoutKeyFailsClosed(t *testing.T) {
	got, err := withoutKey(json.RawMessage(`{"a":1,"maintenance":[2]}`), "maintenance")
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("withoutKey = %s, %v; want {\"a\":1}", got, err)
	}
	got, err = withoutKey(json.RawMessage(`{"a":1}`), "maintenance")
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("withoutKey without the key = %s, %v; want the body unchanged", got, err)
	}
	if got, err := withoutKey(json.RawMessage(`[1,2]`), "maintenance"); err == nil {
		t.Fatalf("withoutKey on a non-object = %s, nil; want an error", got)
	}
}
