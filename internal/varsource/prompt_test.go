package varsource

import (
	"fmt"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// promptNode parses a vars_prompt list; its first entry is on line 1.
func promptNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}

func fromPrompt(line int, f schema.Field) schema.Field {
	f.Origin = schema.OriginSource
	f.Source = &schema.FieldSource{Kind: schema.SourceVarsPrompt, File: "site.yml", Line: line}
	return f
}

func TestPromptFields(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		field *schema.Field // nil: skipped
		notes []string
	}{
		{"public prompt with default", `{name: user, prompt: "User name", private: false, default: admin}`,
			&schema.Field{Name: "user", Label: "User name", Type: schema.TypeString, Default: "admin"}, nil},
		{"private by default", `{name: pass, prompt: Password}`,
			&schema.Field{Name: "pass", Label: "Password", Type: schema.TypeSecret, Required: true}, nil},
		{"secret default dropped", `{name: pin, private: true, default: "1234"}`,
			&schema.Field{Name: "pin", Type: schema.TypeSecret},
			[]string{"warning secret_default_dropped site.yml:1"}},
		// Ansible answers a prompt with a default by that default, so the
		// run does not need a value: not required, though the secret
		// cannot carry the default.
		{"prompt with default, private absent", `{name: pin, prompt: PIN, default: "1234"}`,
			&schema.Field{Name: "pin", Label: "PIN", Type: schema.TypeSecret},
			[]string{"warning secret_default_dropped site.yml:1"}},
		{"prompt without default, private absent", `{name: pin, prompt: PIN}`,
			&schema.Field{Name: "pin", Label: "PIN", Type: schema.TypeSecret, Required: true}, nil},
		{"public prompt without default", `{name: host, prompt: Host, private: false}`,
			&schema.Field{Name: "host", Label: "Host", Type: schema.TypeString, Required: true}, nil},
		{"YAML 1.1 private", `{name: user, private: off}`,
			&schema.Field{Name: "user", Type: schema.TypeString, Required: true}, nil},
		{"non-string default is its text", `{name: count, private: false, default: 5}`,
			&schema.Field{Name: "count", Type: schema.TypeString, Default: "5"}, nil},
		{"templated default", `{name: dir, private: false, default: "{{ base }}/x"}`,
			&schema.Field{Name: "dir", Type: schema.TypeString},
			[]string{"hint templated_default site.yml:1"}},
		{"collection default", `{name: dir, private: false, default: [a]}`,
			&schema.Field{Name: "dir", Type: schema.TypeString},
			[]string{"warning default_invalid site.yml:1"}},
		{"no name", `{prompt: Who}`, nil, []string{"warning invalid_name site.yml:1"}},
		{"bad name", `{name: Who}`, nil, []string{"warning invalid_name site.yml:1"}},
		{"not a mapping", `just text`, nil, []string{"warning field_invalid site.yml:1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields, notes, _ := promptFields(newYAMLFile("site.yml"), promptNode(t, "- "+tc.entry+"\n"))
			var want []schema.Field
			if tc.field != nil {
				want = []schema.Field{fromPrompt(1, *tc.field)}
			}
			wantFields(t, fields, want)
			wantNotes(t, notes, tc.notes...)
		})
	}
}

func TestPromptFieldsBlockStyleAndLimits(t *testing.T) {
	t.Run("block entries keep their lines", func(t *testing.T) {
		fields, notes, _ := promptFields(newYAMLFile("site.yml"), promptNode(t, "- name: a\n  private: no\n- name: b\n  prompt: B\n"))
		wantFields(t, fields, []schema.Field{
			fromPrompt(1, schema.Field{Name: "a", Type: schema.TypeString, Required: true}),
			fromPrompt(3, schema.Field{Name: "b", Label: "B", Type: schema.TypeSecret, Required: true}),
		})
		wantNotes(t, notes)
	})
	t.Run("not a list", func(t *testing.T) {
		fields, notes, _ := promptFields(newYAMLFile("site.yml"), promptNode(t, "name: a\n"))
		wantFields(t, fields, nil)
		wantNotes(t, notes, "warning field_invalid site.yml:1")
	})
	t.Run("101 prompts", func(t *testing.T) {
		src := ""
		for i := range 101 {
			src += fmt.Sprintf("- {name: p%03d, private: false, default: x}\n", i)
		}
		fields, notes, _ := promptFields(newYAMLFile("site.yml"), promptNode(t, src))
		if len(fields) != 100 || fields[99].Name != "p099" {
			t.Fatalf("got %d fields, want the first 100", len(fields))
		}
		wantNotes(t, notes, "warning too_many_fields site.yml:101")
	})
}
