package assistantchange_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
)

func TestNewCarriesActorIdentityAndFields(t *testing.T) {
	user, token, id := uuid.New(), uuid.New(), uuid.New()
	fields := []assistantchange.Field{{Name: "extra_vars.port", Old: 80, New: 8080}}

	c := assistantchange.New(assistantchange.SubjectTemplate, id, "deploy", fields,
		assistantchange.Actor{UserID: user, TokenID: &token, Assistant: true})

	if c.Subject != assistantchange.SubjectTemplate || c.ID != id || c.Name != "deploy" {
		t.Fatalf("subject, id or name not carried: %+v", c)
	}
	if c.UserID != user || c.TokenID == nil || *c.TokenID != token {
		t.Fatalf("actor identity not carried: %+v", c)
	}
	if len(c.Fields) != 1 || c.Fields[0].Name != "extra_vars.port" {
		t.Fatalf("fields not carried: %+v", c.Fields)
	}
	if c.Reach != nil || c.HostID != nil {
		t.Fatalf("New must leave reach and host unset: %+v", c)
	}
}

func TestNewWithoutToken(t *testing.T) {
	c := assistantchange.New(assistantchange.SubjectRepo, uuid.New(), "repo", nil,
		assistantchange.Actor{UserID: uuid.New()})
	if c.TokenID != nil {
		t.Fatalf("TokenID = %v, want nil", c.TokenID)
	}
}

func TestChangeJSONShape(t *testing.T) {
	c := assistantchange.New(assistantchange.SubjectInventoryGroup, uuid.New(), "web",
		[]assistantchange.Field{{Name: "vars.a", Old: nil, New: "x"}},
		assistantchange.Actor{UserID: uuid.New()})
	c.Reach = &assistantchange.Reach{Hosts: []uuid.UUID{uuid.New()}}

	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["subject"] != "inventory_group" {
		t.Errorf("subject = %v", m["subject"])
	}
	if _, ok := m["reach"]; ok {
		t.Error("reach must not be stored")
	}
	for _, k := range []string{"host_id", "host_name", "token_id"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s must be omitted when empty", k)
		}
	}
	f := m["fields"].([]any)[0].(map[string]any)
	if f["field"] != "vars.a" || f["old"] != nil || f["new"] != "x" {
		t.Errorf("field = %v", f)
	}
}
