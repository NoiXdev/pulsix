package varsync_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/varsource"
	"github.com/noixdev/pulsix/internal/varsync"
)

// Review Focus 1: a field turns gone only when the source that declared it
// was read completely and no longer declares it. Anything not read keeps
// the template's state and the preview says why.
func TestComputeGoneOnlyFromReadSources(t *testing.T) {
	port := roleArg("nginx", "nginx_port", schema.TypeInt)
	release := marker("release", schema.TypeString, 3)
	region := sidecar("region", schema.TypeString)
	keep := marker("keep", schema.TypeString, 6) // still declared in every case
	current := []schema.Field{stored(port, true), stored(release, true), stored(region, false), stored(keep, true)}

	roles := func(uses ...varsource.RoleUse) func(*varsync.Source) {
		return func(s *varsync.Source) { s.Playbook.Roles = uses }
	}
	files := func(uses ...varsource.FileUse) func(*varsync.Source) {
		return func(s *varsync.Source) {
			s.Playbook.Files = append([]varsource.FileUse{{Path: playbookPath, State: varsource.FileRead}}, uses...)
		}
	}
	role := func(name string, state varsource.RoleState) varsource.RoleUse {
		return varsource.RoleUse{Name: name, EntryPoint: "main", State: state}
	}
	file := func(path string, state varsource.FileState) varsource.FileUse {
		return varsource.FileUse{Path: path, State: state}
	}
	all := []string{"nginx_port", "release", "region"}
	cases := []struct {
		name string
		mod  []func(*varsync.Source)
		gone []string
		note string // the warning the kept fields raise, "" when none are kept
	}{
		{"role read, names absent", nil, all, ""},
		{"role without specs", []func(*varsync.Source){roles(role("nginx", varsource.RoleNoSpecs))}, all, ""},
		{"role no longer used, no dynamic role", []func(*varsync.Source){roles()}, all, ""},
		{"role not found", []func(*varsync.Source){roles(role("nginx", varsource.RoleNotFound))}, nil, "not_checked"},
		{"role unreadable", []func(*varsync.Source){roles(role("nginx", varsource.RoleUnreadable))}, nil, "not_checked"},
		{"an unreadable role use without a name", []func(*varsync.Source){roles(role("nginx", varsource.RoleRead), role("", varsource.RoleUnreadable))}, nil, "not_checked"},
		{"role no longer used, a dynamic role present", []func(*varsync.Source){roles(role("{{ r }}", varsource.RoleDynamic))}, nil, "not_checked"},
		{"a collection role", []func(*varsync.Source){roles(role("nginx", varsource.RoleRead), role("ns.col.r", varsource.RoleCollection))}, nil, "not_checked"},
		{"an inline playbook's role", []func(*varsync.Source){roles(role("nginx", varsource.RoleInline))}, nil, "not_checked"},
		{"an imported playbook read", []func(*varsync.Source){files(file("more.yml", varsource.FileRead))}, all, ""},
		{"an import that cycles back to a read file", []func(*varsync.Source){files(file("more.yml", varsource.FileRead), file(playbookPath, varsource.FileCycle))}, all, ""},
		{"a path seen only as a cycle", []func(*varsync.Source){files(file("more.yml", varsource.FileCycle))}, all, ""},
		{"an imported playbook unreadable", []func(*varsync.Source){files(file("more.yml", varsource.FileUnreadable))}, nil, "not_checked"},
		{"an imported playbook missing", []func(*varsync.Source){files(file("more.yml", varsource.FileMissing))}, nil, "not_checked"},
		{"an import that became dynamic", []func(*varsync.Source){files(file("{{ p }}", varsource.FileDynamic))}, nil, "not_checked"},
		{"an import too deep", []func(*varsync.Source){files(file("d.yml", varsource.FileTooDeep))}, nil, "not_checked"},
		{"an import outside the repository", []func(*varsync.Source){files(file("../x.yml", varsource.FileOutside))}, nil, "not_checked"},
		{"a malformed import", []func(*varsync.Source){files(file("import_playbook@7", varsource.FileUnreadable))}, nil, "not_checked"},
		{"a sidecar unreadable", []func(*varsync.Source){files(file("site.argument_specs.yml", varsource.FileUnreadable))}, nil, "not_checked"},
		{"a path read once and unreadable once", []func(*varsync.Source){files(file("more.yml", varsource.FileRead), file("more.yml", varsource.FileUnreadable))}, all, ""},
		{"no file recorded as read", []func(*varsync.Source){func(s *varsync.Source) { s.Playbook.Files = nil }}, nil, "not_checked"},
		{"a cap left fields out", []func(*varsync.Source){func(s *varsync.Source) { s.Playbook.FieldsCut = true }}, nil, "not_checked"},
		{"the playbook was not read", []func(*varsync.Source){func(s *varsync.Source) {
			s.Playbook.Failed, s.Playbook.Fields = true, nil
			s.Playbook.Files = []varsource.FileUse{file(playbookPath, varsource.FileUnreadable)}
		}}, nil, "not_checked"},
		{"a name declared but unreadable", []func(*varsync.Source){func(s *varsync.Source) { s.Playbook.Unreadable = []string{"release"} }},
			[]string{"nginx_port", "region"}, "not_checked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := source(keep)
			for _, m := range tc.mod {
				m(&src)
			}
			if src.Playbook.Failed {
				src.Playbook.Fields = nil
			}
			p := varsync.Compute(current, src, nil)
			if got := goneNames(p); !reflect.DeepEqual(got, append([]string{}, tc.gone...)) {
				t.Errorf("gone = %v, want %v", got, tc.gone)
			}
			if got := hasCode(p, "not_checked"); got != (tc.note != "") {
				t.Errorf("notes = %v, want not_checked %v", noteCodes(p), tc.note != "")
			}
			if tc.note != "" {
				kept := 3 - len(tc.gone)
				if src.Playbook.Failed {
					kept++ // keep is not read either
				}
				n := p.Notes[0]
				if n.Code != "not_checked" || n.Level != varsource.LevelWarning || !strings.Contains(n.Message, fmt.Sprintf("%d field", kept)) {
					t.Errorf("note = %+v, want a warning counting %d kept fields", n, kept)
				}
			}
			for _, g := range p.Gone {
				if g.Field.Origin != schema.OriginGone || g.Field.Source == nil {
					t.Errorf("gone %s = %s", g.Name, jsonOf(t, g.Field))
				}
			}
			out := varsync.Apply(current, p, varsync.ApplyOptions{Automatic: true, RemoveGone: true})
			if len(out) != len(current) {
				t.Fatalf("automatic apply removed fields: %s", jsonOf(t, out))
			}
			for _, f := range out {
				isGone := false
				for _, g := range tc.gone {
					isGone = isGone || g == f.Name
				}
				if want := map[bool]schema.Origin{true: schema.OriginGone, false: schema.OriginSource}[isGone]; f.Origin != want {
					t.Errorf("%s: origin %q, want %q", f.Name, f.Origin, want)
				}
			}
		})
	}
}

// A source that is not available names nothing gone, whatever it holds:
// each reason a playbooks.VarSource is not usable makes one.
func TestNewSourceAvailability(t *testing.T) {
	pb := varsource.Playbook{Path: playbookPath, Files: []varsource.FileUse{{Path: playbookPath, State: varsource.FileRead}}}
	failed := pb
	failed.Failed = true
	cases := []struct {
		name   string
		read   varsync.Read
		avail  bool
		reason string
	}{
		{"read", varsync.Read{Rev: "c1", Found: true, WorkerReads: true, Playbook: pb}, true, ""},
		{"no record", varsync.Read{Playbook: pb}, false, "no variable sources"},
		{"not found in the record", varsync.Read{Rev: "c1", WorkerReads: true}, false, "no variable sources"},
		{"an older worker", varsync.Read{Rev: "c1", Found: true, Playbook: pb}, false, "does not read variable sources"},
		{"left out", varsync.Read{Found: true, WorkerReads: true, LeftOut: true, Playbook: pb}, false, "left this playbook out"},
		{"failed", varsync.Read{Rev: "c1", Found: true, WorkerReads: true, Playbook: failed}, false, "could not be read"},
	}
	current := []schema.Field{stored(marker("release", schema.TypeString, 3), true)}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := varsync.NewSource(tc.read)
			if src.Available != tc.avail || !strings.Contains(src.Reason, tc.reason) || src.Rev != tc.read.Rev {
				t.Fatalf("source = %+v, want available %v reason %q", src, tc.avail, tc.reason)
			}
			p := varsync.Compute(current, src, nil)
			if tc.avail {
				if got := goneNames(p); !reflect.DeepEqual(got, []string{"release"}) {
					t.Errorf("gone = %v", got)
				}
				return
			}
			if len(p.Gone) != 0 || !hasCode(p, "source_unavailable") {
				t.Errorf("preview = %s, want nothing gone and source_unavailable", jsonOf(t, p))
			}
		})
	}
}

// A field already gone that the source still lacks is listed again (it can
// be removed); one the source cannot vouch for stays out of the list.
func TestComputeListsGoneFieldsAgain(t *testing.T) {
	old := gone(stored(marker("release", schema.TypeString, 3), true))

	p := varsync.Compute([]schema.Field{old}, source(), nil)
	if got := goneNames(p); !reflect.DeepEqual(got, []string{"release"}) {
		t.Errorf("gone = %v", got)
	}

	src := source()
	src.Playbook.Roles = []varsource.RoleUse{{Name: "nginx", EntryPoint: "main", State: varsource.RoleNotFound}}
	p = varsync.Compute([]schema.Field{old}, src, nil)
	if len(p.Gone) != 0 || !hasCode(p, "not_checked") {
		t.Errorf("preview = %s, want nothing gone and not_checked", jsonOf(t, p))
	}
	out := varsync.Apply([]schema.Field{old}, p, varsync.ApplyOptions{RemoveGone: true})
	if len(out) != 1 || out[0].Origin != schema.OriginGone {
		t.Errorf("applied = %s, want the gone field kept", jsonOf(t, out))
	}
}

func TestComputeGoneKeepsTheSourceVersion(t *testing.T) {
	src80 := roleArg("nginx", "nginx_port", schema.TypeInt, func(f *schema.Field) { f.Default = 80 })
	mine := src80
	mine.Label = "Port"
	cur := adjusted(stored(mine, false), src80)

	p := varsync.Compute([]schema.Field{cur}, source(), nil)

	if len(p.Gone) != 1 {
		t.Fatalf("gone = %s", jsonOf(t, p.Gone))
	}
	out := varsync.Apply([]schema.Field{cur}, p, varsync.ApplyOptions{Automatic: true})
	f := field(t, out, "nginx_port")
	if f.Origin != schema.OriginGone || f.SourceField == nil || f.Label != "Port" || *f.Active {
		t.Errorf("applied = %s", jsonOf(t, f))
	}
	if err := (schema.Schema{Fields: out}).Check(); err != nil {
		t.Errorf("applied schema does not pass Check: %v", err)
	}
}
