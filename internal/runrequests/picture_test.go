package runrequests_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
)

func TestFingerprintCoversEveryField(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	base := runrequests.Picture{
		TemplateID: "t", TemplateName: "deploy", TemplateRevision: 1, Limit: "web", OnlyHost: "",
		Playbook: runrequests.PicturePlaybook{Source: "repo", Path: "site.yml"},
		Repo: &runrequests.PictureRepo{Name: "infra", URL: "https://git.example/infra", Branch: "main", Subpath: "ansible",
			GitCredentialID: "g1", GitCredential: "git-deploy"},
		Targets: []runrequests.PictureHost{
			{ID: "b", Name: "web-2", Address: "10.0.0.2", SSHUser: "root", SSHPort: 22, CredentialID: "c1", Credential: "host-login"},
			{ID: "a", Name: "web-1", Address: "10.0.0.1", SSHUser: "root", SSHPort: 22, JumpHost: "bastion", JumpHostID: "j1"},
		},
		SSHCredential: "deploy-key", SSHCredentialID: "s1",
		VaultIdentities: []runrequests.PictureVault{{Label: "noidee", CredentialID: "v1", Credential: "vault"}},
		ExtraVars:       map[string]any{"a": 1},
		VarsDigest:      "d1",
	}
	fp := runrequests.Fingerprint(id, base)
	if fp != runrequests.Fingerprint(id, base) {
		t.Fatal("not deterministic")
	}
	reordered := base
	reordered.Targets = []runrequests.PictureHost{base.Targets[1], base.Targets[0]}
	if runrequests.Fingerprint(id, reordered) != fp {
		t.Fatal("host order changes the fingerprint")
	}
	if runrequests.Fingerprint(uuid.Must(uuid.NewV7()), base) == fp {
		t.Fatal("the request id is not covered")
	}
	mutations := map[string]func(p *runrequests.Picture){
		"template name": func(p *runrequests.Picture) { p.TemplateName = "x" },
		"revision":      func(p *runrequests.Picture) { p.TemplateRevision = 2 },
		"limit":         func(p *runrequests.Picture) { p.Limit = "db" },
		"only host":     func(p *runrequests.Picture) { p.OnlyHost = "a" },
		"playbook":      func(p *runrequests.Picture) { p.Playbook.Path = "other.yml" },
		"repo url":      func(p *runrequests.Picture) { r := *p.Repo; r.URL = "https://evil.example/x"; p.Repo = &r },
		"repo branch":   func(p *runrequests.Picture) { r := *p.Repo; r.Branch = "dev"; p.Repo = &r },
		"repo subpath":  func(p *runrequests.Picture) { r := *p.Repo; r.Subpath = ""; p.Repo = &r },
		"target added": func(p *runrequests.Picture) {
			p.Targets = append(append([]runrequests.PictureHost{}, p.Targets...), runrequests.PictureHost{ID: "c"})
		},
		"target address": func(p *runrequests.Picture) {
			p.Targets = append([]runrequests.PictureHost{}, p.Targets...)
			p.Targets[0].Address = "10.9.9.9"
		},
		"ssh credential":    func(p *runrequests.Picture) { p.SSHCredential = "other" },
		"ssh credential id": func(p *runrequests.Picture) { p.SSHCredentialID = "s2" },
		"repo git credential id": func(p *runrequests.Picture) {
			r := *p.Repo
			r.GitCredentialID = "g2"
			p.Repo = &r
		},
		"repo git credential": func(p *runrequests.Picture) { r := *p.Repo; r.GitCredential = "other"; p.Repo = &r },
		"target credential id": func(p *runrequests.Picture) {
			p.Targets = append([]runrequests.PictureHost{}, p.Targets...)
			p.Targets[0].CredentialID = "c2"
		},
		"target jump host id": func(p *runrequests.Picture) {
			p.Targets = append([]runrequests.PictureHost{}, p.Targets...)
			p.Targets[1].JumpHostID = "j2"
		},
		"vault credential id": func(p *runrequests.Picture) {
			p.VaultIdentities = []runrequests.PictureVault{{Label: "noidee", CredentialID: "v2", Credential: "vault"}}
		},
		"vault":       func(p *runrequests.Picture) { p.VaultIdentities = nil },
		"extra vars":  func(p *runrequests.Picture) { p.ExtraVars = map[string]any{"a": 2} },
		"vars digest": func(p *runrequests.Picture) { p.VarsDigest = "d2" },
		"retry":       func(p *runrequests.Picture) { p.Retry = &runrequests.PictureRetry{JobID: "j"} },
		"follow-ups added": func(p *runrequests.Picture) {
			p.FollowUps = &runrequests.PictureFollowUps{Start: true, Templates: []runrequests.PictureFollowUp{{TemplateID: "a", TemplateName: "a", TemplateRevision: 1}}}
		},
	}
	withFollowUps := base
	withFollowUps.FollowUps = &runrequests.PictureFollowUps{Start: true, Templates: []runrequests.PictureFollowUp{
		{TemplateID: "a", TemplateName: "a", TemplateRevision: 1}, {TemplateID: "b", TemplateName: "b", TemplateRevision: 2},
	}}
	fuFP := runrequests.Fingerprint(id, withFollowUps)
	fuMutations := map[string]func(f *runrequests.PictureFollowUps){
		"start":    func(f *runrequests.PictureFollowUps) { f.Start = false },
		"revision": func(f *runrequests.PictureFollowUps) { f.Templates[0].TemplateRevision = 9 },
		"order": func(f *runrequests.PictureFollowUps) {
			f.Templates[0], f.Templates[1] = f.Templates[1], f.Templates[0]
		},
		"child": func(f *runrequests.PictureFollowUps) {
			f.Templates[0].FollowUps = []runrequests.PictureFollowUp{{TemplateID: "c"}}
		},
		"truncated": func(f *runrequests.PictureFollowUps) { f.Truncated = true },
		"stop":      func(f *runrequests.PictureFollowUps) { f.Templates[1].Stop = runrequests.StopInChain },
	}
	for name, mutate := range fuMutations {
		p := base
		f := *withFollowUps.FollowUps
		f.Templates = append([]runrequests.PictureFollowUp(nil), f.Templates...)
		mutate(&f)
		p.FollowUps = &f
		if runrequests.Fingerprint(id, p) == fuFP {
			t.Errorf("follow-ups %s does not change the fingerprint", name)
		}
	}
	for name, mutate := range mutations {
		p := base
		mutate(&p)
		if runrequests.Fingerprint(id, p) == fp {
			t.Errorf("%s does not change the fingerprint", name)
		}
	}
}
