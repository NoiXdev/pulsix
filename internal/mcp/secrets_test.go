package mcp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

var testFields = []schema.Field{
	{Name: "user", Type: schema.TypeString},
	{Name: "password", Type: schema.TypeSecret},
	{Name: "db", Type: schema.TypeObject, Fields: []schema.Field{
		{Name: "host", Type: schema.TypeString},
		{Name: "token", Type: schema.TypeSecret},
	}},
	{Name: "admins", Type: schema.TypeRepeater, Fields: []schema.Field{
		{Name: "name", Type: schema.TypeString},
		{Name: "key", Type: schema.TypeSecret},
	}},
}

func TestCheckValuesRefusesSecretValues(t *testing.T) {
	cases := map[string]map[string]any{
		"top level":     {"password": "hunter2"},
		"nested object": {"db": map[string]any{"token": "abc"}},
		"repeater item": {"admins": []any{map[string]any{"name": "a", "key": "ssh-ed25519 AAAA"}}},
		"sealed value":  {"password": map[string]any{"$sealed": "AAAA"}},
	}
	for name, values := range cases {
		if _, err := checkValues([][]schema.Field{testFields}, values, keepGroup); err == nil || !strings.Contains(err.Error(), "secrets are set by a person in Pulsix") {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
}

func TestCheckValuesTranslatesKeep(t *testing.T) {
	values := map[string]any{
		"user":     "deploy",
		"password": map[string]any{"$secret": "keep"},
		"db":       map[string]any{"host": "db1", "token": map[string]any{"$secret": "set"}},
	}
	got, err := checkValues([][]schema.Field{testFields}, values, keepGroup)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["password"], map[string]any{"$secret": "keep", "from": "password"}) {
		t.Errorf("group keep = %v", got["password"])
	}
	if !reflect.DeepEqual(got["db"].(map[string]any)["token"], map[string]any{"$secret": "keep", "from": "db.token"}) {
		t.Errorf("nested group keep = %v", got["db"])
	}
	got, err = checkValues([][]schema.Field{testFields}, map[string]any{"password": map[string]any{"$secret": "keep"}}, keepTemplate)
	if err != nil || !reflect.DeepEqual(got["password"], map[string]any{"$secret": "set"}) {
		t.Errorf("template keep = %v %v", got, err)
	}
}

// TestCheckValuesUsesTheUnionOfSchemas: a field that is secret in either
// the stored or the submitted schema is secret.
func TestCheckValuesUsesTheUnionOfSchemas(t *testing.T) {
	plain := []schema.Field{{Name: "password", Type: schema.TypeString}}
	secret := []schema.Field{{Name: "password", Type: schema.TypeSecret}}
	for _, sets := range [][][]schema.Field{{plain, secret}, {secret, plain}} {
		if _, err := checkValues(sets, map[string]any{"password": "x"}, keepGroup); err == nil {
			t.Errorf("a value under a field secret in one schema passed: %v", sets)
		}
	}
	if _, err := checkValues([][]schema.Field{plain}, map[string]any{"password": "x"}, keepGroup); err != nil {
		t.Errorf("a plain field was refused: %v", err)
	}
}

func TestCheckSchemaChange(t *testing.T) {
	added := append(append([]schema.Field{}, testFields...), schema.Field{Name: "api_key", Type: schema.TypeSecret})
	if err := checkSchemaChange(testFields, added); err != nil {
		t.Errorf("adding a secret field was refused: %v", err)
	}
	if err := checkSchemaChange(nil, testFields); err != nil {
		t.Errorf("a first schema was refused: %v", err)
	}
	typeRemoved := []schema.Field{{Name: "password", Type: schema.TypeString}}
	if err := checkSchemaChange([]schema.Field{{Name: "password", Type: schema.TypeSecret}}, typeRemoved); err == nil || !strings.Contains(err.Error(), "secret fields keep their type") {
		t.Errorf("removing the secret type = %v", err)
	}
	nestedRemoved := []schema.Field{testFields[0], testFields[1], {Name: "db", Type: schema.TypeObject, Fields: []schema.Field{{Name: "host", Type: schema.TypeString}}}, testFields[3]}
	if err := checkSchemaChange(testFields, nestedRemoved); err == nil || !strings.Contains(err.Error(), "db.token") {
		t.Errorf("removing a nested secret field = %v", err)
	}
}
