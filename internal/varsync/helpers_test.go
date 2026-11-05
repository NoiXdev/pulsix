package varsync_test

import (
	"encoding/json"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/varsource"
	"github.com/noixdev/pulsix/internal/varsync"
)

const playbookPath = "site.yml"

// marker is a candidate field read from an inline marker in site.yml.
func marker(name string, t schema.Type, line int, mod ...func(*schema.Field)) schema.Field {
	f := schema.Field{Name: name, Type: t, Origin: schema.OriginSource,
		Source: &schema.FieldSource{Kind: schema.SourceMarker, File: playbookPath, Line: line}}
	for _, m := range mod {
		m(&f)
	}
	return f
}

// roleArg is a candidate field read from role's argument specs (entry
// point main).
func roleArg(role, name string, t schema.Type, mod ...func(*schema.Field)) schema.Field {
	f := schema.Field{Name: name, Type: t, Origin: schema.OriginSource,
		Source: &schema.FieldSource{Kind: schema.SourceArgumentSpec, Role: role, EntryPoint: "main",
			File: "roles/" + role + "/meta/argument_specs.yml", Line: 3}}
	for _, m := range mod {
		m(&f)
	}
	return f
}

// sidecar is a candidate field read from site.argument_specs.yml.
func sidecar(name string, t schema.Type) schema.Field {
	return schema.Field{Name: name, Type: t, Origin: schema.OriginSource,
		Source: &schema.FieldSource{Kind: schema.SourceSidecar, EntryPoint: "main", File: "site.argument_specs.yml", Line: 4}}
}

// stored is the template's field for a candidate as an earlier apply left
// it: the candidate itself with an explicit active switch.
func stored(c schema.Field, active bool) schema.Field {
	c.Active = &active
	return c
}

// adjusted is a template field a person adjusted: content, with the source's
// version of it.
func adjusted(content, source schema.Field) schema.Field {
	content.Origin = schema.OriginAdjusted
	sf := source
	sf.Origin, sf.Source, sf.Active, sf.SetInPlaybook = "", nil, nil, false
	content.SourceField = &sf
	return content
}

func gone(f schema.Field) schema.Field {
	f.Origin = schema.OriginGone
	return f
}

func manual(name string, t schema.Type) schema.Field {
	return schema.Field{Name: name, Type: t}
}

// source is an available, fully read source of site.yml with the given
// candidates, using role nginx.
func source(fields ...schema.Field) varsync.Source {
	return varsync.Source{Rev: "c1", Available: true, Playbook: varsource.Playbook{
		Path:   playbookPath,
		Fields: fields,
		Roles:  []varsource.RoleUse{{Name: "nginx", EntryPoint: "main", State: varsource.RoleRead}},
		Files:  []varsource.FileUse{{Path: playbookPath, State: varsource.FileRead}},
		Notes:  []varsource.Note{},
	}}
}

func ptr[T any](v T) *T { return &v }

func names[T any](items []T, name func(T) string) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, name(it))
	}
	return out
}

func addedNames(p varsync.Preview) []string {
	return names(p.Added, func(a varsync.Added) string { return a.Field.Name })
}

func goneNames(p varsync.Preview) []string {
	return names(p.Gone, func(g varsync.Gone) string { return g.Name })
}

func noteCodes(p varsync.Preview) []string {
	return names(p.Notes, func(n varsource.Note) string { return n.Code })
}

func hasCode(p varsync.Preview, code string) bool {
	for _, c := range noteCodes(p) {
		if c == code {
			return true
		}
	}
	return false
}

// jsonOf is v as canonical JSON, for comparing values that may have
// round-tripped through JSON.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %v: %v", v, err)
	}
	return string(b)
}

func field(t *testing.T, fields []schema.Field, name string) schema.Field {
	t.Helper()
	for _, f := range fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no field %s in %s", name, jsonOf(t, fields))
	return schema.Field{}
}
