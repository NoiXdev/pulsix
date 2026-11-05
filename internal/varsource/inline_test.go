package varsource

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

func TestParseInline(t *testing.T) {
	text := `- hosts: all
  vars:
    port: 8080  # pulsix: Port
  vars_prompt:
    - name: user
      private: false
  roles:
    - web
    - "{{ dyn }}"
- import_playbook: other.yml
`
	pb := ParseInline(text)
	if pb.Path != "inline" || pb.Failed {
		t.Fatalf("path %q, failed %v", pb.Path, pb.Failed)
	}
	wantFields(t, pb.Fields, []schema.Field{
		{Name: "port", Type: schema.TypeInt, Default: 8080, Label: "Port", Origin: schema.OriginSource,
			Source: &schema.FieldSource{Kind: schema.SourceMarker, File: "inline", Line: 3}},
		{Name: "user", Type: schema.TypeString, Required: true, Origin: schema.OriginSource,
			Source: &schema.FieldSource{Kind: schema.SourceVarsPrompt, File: "inline", Line: 5}},
	})
	wantRoles(t, pb.Roles,
		RoleUse{Name: "web", EntryPoint: "main", State: RoleInline},
		RoleUse{Name: "{{ dyn }}", EntryPoint: "main", State: RoleDynamic},
	)
	wantFiles(t, pb.Files,
		FileUse{Path: "inline", State: FileRead},
		FileUse{Path: "other.yml", State: FileDynamic},
	)
	wantNotes(t, pb.Notes,
		"hint inline_roles_unresolved inline:8",
		"hint role_dynamic inline:9",
		"hint inline_import_unresolved inline:10",
	)
}

func TestParseInlineFailures(t *testing.T) {
	for name, tc := range map[string]struct{ text, note string }{
		"invalid":   {"- hosts: [\n", "warning invalid_yaml inline:1"},
		"too large": {"- hosts: all\n#" + strings.Repeat("x", MaxFileBytes), "warning file_too_large inline:0"},
		"not plays": {"a: 1\n", "warning not_a_playbook inline:0"},
	} {
		t.Run(name, func(t *testing.T) {
			pb := ParseInline(tc.text)
			if !pb.Failed {
				t.Fatal("not failed")
			}
			wantNotes(t, pb.Notes, tc.note)
		})
	}
}
