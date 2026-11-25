package runrequests_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
)

// goldenPicture is a picture of a request made before run requests carried
// overrides: every field it has existed then.
func goldenPicture() runrequests.Picture {
	return runrequests.Picture{
		TemplateID: "t", TemplateName: "deploy", TemplateRevision: 4, Limit: "web", OnlyHost: "h1",
		Playbook:      runrequests.PicturePlaybook{ID: "p", Source: "repo", Path: "site.yml"},
		Repo:          &runrequests.PictureRepo{ID: "r", Name: "infra", URL: "https://git.example/infra", Branch: "main", Subpath: "ansible"},
		Targets:       []runrequests.PictureHost{{ID: "h1", Name: "web-1", Address: "10.0.0.1", SSHUser: "root", SSHPort: 22}},
		SSHCredential: "deploy-key", SSHCredentialID: "s1",
		VaultIdentities: []runrequests.PictureVault{{Label: "prod", CredentialID: "v1", Credential: "vault"}},
		ExtraVars:       map[string]any{"tag": "v1", "pw": map[string]any{"$secret": "set"}},
		VarsDigest:      "d1",
		FollowUps: &runrequests.PictureFollowUps{Start: true, Templates: []runrequests.PictureFollowUp{
			{TemplateID: "f", TemplateName: "notify", TemplateRevision: 2},
		}},
	}
}

// goldenFingerprint is goldenPicture's fingerprint as the code computed it
// before the override fields existed (2026-11-25): a request pending across
// the upgrade keeps the fingerprint its reviewer saw.
const goldenFingerprint = "c3a864aa823d8e26ff251fcb1d88fe10470730ef4a7b4b1f4ec9c62cbb25b1d1"

var goldenRequest = uuid.MustParse("0192f000-0000-7000-8000-000000000001")

func TestFingerprintOfAPictureWithoutOverridesIsUnchanged(t *testing.T) {
	if got := runrequests.Fingerprint(goldenRequest, goldenPicture()); got != goldenFingerprint {
		t.Fatalf("fingerprint = %s, want %s: the canonical JSON of a picture without overrides changed", got, goldenFingerprint)
	}
}

func TestFingerprintCoversTheOverrides(t *testing.T) {
	base := goldenPicture()
	base.CheckMode, base.Diff, base.LimitOverride = true, true, "web-1"
	base.ExtraVarsOverride = map[string]any{"version": "1.2"}
	base.Hosts = []string{"h2", "h1"}
	fp := runrequests.Fingerprint(goldenRequest, base)

	reordered := base
	reordered.Hosts = []string{"h1", "h2"}
	if runrequests.Fingerprint(goldenRequest, reordered) != fp {
		t.Fatal("the order of the requested hosts changes the fingerprint")
	}
	for name, mutate := range map[string]func(p *runrequests.Picture){
		"check mode":     func(p *runrequests.Picture) { p.CheckMode = false },
		"diff":           func(p *runrequests.Picture) { p.Diff = false },
		"limit override": func(p *runrequests.Picture) { p.LimitOverride = "web-2" },
		"extra vars":     func(p *runrequests.Picture) { p.ExtraVarsOverride = map[string]any{"version": "1.3"} },
		"hosts":          func(p *runrequests.Picture) { p.Hosts = []string{"h1"} },
	} {
		p := base
		mutate(&p)
		if runrequests.Fingerprint(goldenRequest, p) == fp {
			t.Errorf("%s does not change the fingerprint", name)
		}
	}
}
