package runner

import (
	"testing"

	"github.com/google/uuid"
)

func TestRepoSubpath(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name     string
		claimed  *string
		subpath  string
		playbook string
		want     string
		wantErr  string
	}{
		{"claimed", str("ansible"), "ansible/site.yml", "site.yml", "ansible", ""},
		{"claimed empty", str(""), "site.yml", "site.yml", "", ""},
		{"claimed with a trailing slash", str("ansible/"), "ansible//site.yml", "site.yml", "ansible", ""},
		{"claimed with a leading slash", str("/ansible"), "/ansible/site.yml", "site.yml", "ansible", ""},
		{"claimed dot", str("."), "./site.yml", "site.yml", "", ""},
		{"derived at the root", nil, "site.yml", "site.yml", "", ""},
		{"derived one level", nil, "ansible/site.yml", "site.yml", "ansible", ""},
		{"derived nested", nil, "a/b/playbooks/site.yml", "playbooks/site.yml", "a/b", ""},
		{"derived from an uncleaned subpath", nil, "ansible//./site.yml", "site.yml", "ansible", ""},
		{
			"subpath not ending with the playbook", nil, "other.yml", "site.yml", "",
			`the claim's subpath "other.yml" does not end with the playbook path "site.yml"`,
		},
		{
			"suffix is not a whole path element", nil, "ansible/mysite.yml", "site.yml", "",
			`the claim's subpath "ansible/mysite.yml" does not end with the playbook path "site.yml"`,
		},
		{
			"claimed but subpath not ending with the playbook", str("ansible"), "ansible/other.yml", "site.yml", "",
			`the claim's subpath "ansible/other.yml" does not end with the playbook path "site.yml"`,
		},
		{
			"empty playbook path", nil, "ansible/", ".", "",
			`the claim's subpath "ansible/" does not end with the playbook path "."`,
		},
		{"claimed leaves the clone", str("../x"), "../x/site.yml", "site.yml", "", `the repository subpath "../x" leads out of the repository`},
		{"claimed leaves the clone after cleaning", str("a/../.."), "a/../../site.yml", "site.yml", "", `the repository subpath "a/../.." leads out of the repository`},
		{"derived leaves the clone", nil, "../site.yml", "site.yml", "", `the repository subpath ".." leads out of the repository`},
		{
			"claimed differs from the subpath", str("other"), "ansible/site.yml", "site.yml", "",
			`the claim's repo subpath "other" does not match its subpath "ansible/site.yml"`,
		},
		{
			"claimed empty but the subpath has one", str(""), "ansible/site.yml", "site.yml", "",
			`the claim's repo subpath "" does not match its subpath "ansible/site.yml"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repoSubpath(tc.claimed, tc.subpath, tc.playbook)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("repoSubpath = %q, %v; want error %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("repoSubpath = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestRepoSpecReadsTheRepoSubpath(t *testing.T) {
	repo := uuid.Must(uuid.NewV7())
	payload := func(extra string) string {
		return `{"repo":{"id":"` + repo.String() + `","url":"file:///x","branch":"main","subpath":"ansible/playbooks/site.yml"` + extra + `}}`
	}
	cases := []struct {
		name    string
		payload string
		want    string
		wantErr bool
	}{
		{"claimed", payload(`,"repo_subpath":"ansible"`), "ansible", false},
		{"claimed empty differs from the subpath", payload(`,"repo_subpath":""`), "", true},
		{"older manager: derived", payload(""), "ansible", false},
		{"claimed out of the clone", payload(`,"repo_subpath":"../ansible"`), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := runJob(t)
			job.KindPayload = tc.payload
			spec, _, err := repoSpec(job, repo, "playbooks/site.yml")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("repoSpec = %+v, want an error", spec)
				}
				return
			}
			if err != nil || spec.RepoSubpath != tc.want {
				t.Fatalf("RepoSubpath = %q, %v; want %q", spec.RepoSubpath, err, tc.want)
			}
		})
	}

	// A subpath that does not end with the playbook path fails the claim.
	job := runJob(t)
	job.KindPayload = payload(`,"repo_subpath":"ansible"`)
	if _, _, err := repoSpec(job, repo, "other.yml"); err == nil {
		t.Fatal("a subpath not ending with the playbook path must fail")
	}
}
