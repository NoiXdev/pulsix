package api

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/varsource"
)

// varSourceNoteDTO is one finding of the variable source parser; code is
// the contract the user documentation lists, message free text.
type varSourceNoteDTO struct {
	Level   string `json:"level" enum:"warning,hint"`
	Code    string `json:"code"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

func toVarSourceNoteDTOs(notes []varsource.Note) []varSourceNoteDTO {
	out := make([]varSourceNoteDTO, len(notes))
	for i, n := range notes {
		out[i] = varSourceNoteDTO{Level: string(n.Level), Code: n.Code, File: n.File, Line: n.Line, Message: n.Message}
	}
	return out
}

// repoSourcesDTO is what a repository's last sync read, apart from its
// playbooks (repos.SourcesRecord).
type repoSourcesDTO struct {
	Commit      string             `json:"commit" doc:"The commit the read saw; empty when no commit was ever read"`
	SyncedAt    time.Time          `json:"synced_at"`
	WorkerReads bool               `json:"worker_reads" doc:"False when the last sync came from a worker that reads no variable sources; the record is the read before"`
	Incomplete  bool               `json:"incomplete" doc:"A limit left playbooks unread"`
	LeftOut     []string           `json:"left_out" doc:"Names the limits left unread; they keep their previous read, if any"`
	Notes       []varSourceNoteDTO `json:"notes"`
	Vault       *repoVaultDTO      `json:"vault,omitempty" doc:"The variables the read found set to values encrypted with Ansible Vault, by name and file, never a value; absent when the read did not report them (a worker older than this feature), so they are unknown"`
}

// repoVaultDTO is varsource.Vault: names, never values.
type repoVaultDTO struct {
	Names          []repoVaultNameDTO `json:"names" doc:"Variables set to a vault-encrypted value, by name then file"`
	EncryptedFiles []string           `json:"encrypted_files" doc:"Variable files encrypted as a whole: the names they set are not known"`
	Unread         []string           `json:"unread" doc:"Variable files that could not be read: the names they set are not known"`
	Cut            bool               `json:"cut" doc:"A limit or the time limit left files unread: names may be missing"`
}

type repoVaultNameDTO struct {
	Name string `json:"name"`
	File string `json:"file" doc:"The file that sets it, relative to the repository subpath"`
}

func toRepoVaultDTO(v *varsource.Vault) *repoVaultDTO {
	if v == nil {
		return nil
	}
	out := &repoVaultDTO{Names: make([]repoVaultNameDTO, len(v.Names)), EncryptedFiles: v.EncryptedFiles, Unread: v.Unread, Cut: v.Cut}
	for i, n := range v.Names {
		out.Names[i] = repoVaultNameDTO{Name: n.Name, File: n.File}
	}
	if out.EncryptedFiles == nil {
		out.EncryptedFiles = []string{}
	}
	if out.Unread == nil {
		out.Unread = []string{}
	}
	return out
}

type discoveredPlaybookDTO struct {
	Path     string `json:"path"`
	Fields   int    `json:"fields"`
	Warnings int    `json:"warnings"`
	Failed   bool   `json:"failed" doc:"The playbook was not read"`
	LeftOut  bool   `json:"left_out" doc:"The last sync left it unread; the counts are from the read before"`
	// PlaybookID is the playbook registered for the path, absent when none
	// is.
	PlaybookID string `json:"playbook_id,omitempty" format:"uuid"`
}

type repoPlaybooksInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type repoPlaybooksOutput struct {
	Body struct {
		Sources   *repoSourcesDTO         `json:"sources,omitempty" doc:"Absent until a sync recorded what it read"`
		Playbooks []discoveredPlaybookDTO `json:"playbooks"`
	}
}

type repoPlaybookPickInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		Path string `json:"path" doc:"The playbook's path inside the repository"`
	}
}

type repoPlaybookPickOutput struct {
	Body struct {
		Playbook playbookDTO `json:"playbook"`
		Created  bool        `json:"created" doc:"False when the path was already registered; that playbook is returned"`
	}
}

func toRepoPlaybooksOutput(d playbooks.Discovery) *repoPlaybooksOutput {
	out := &repoPlaybooksOutput{}
	if r := d.Record; r != nil {
		leftOut := r.LeftOut
		if leftOut == nil {
			leftOut = []string{}
		}
		out.Body.Sources = &repoSourcesDTO{
			Commit: r.Commit, SyncedAt: r.SyncedAt, WorkerReads: r.WorkerReads, Incomplete: r.Incomplete,
			LeftOut: leftOut, Notes: toVarSourceNoteDTOs(r.Notes), Vault: toRepoVaultDTO(r.Vault),
		}
	}
	out.Body.Playbooks = make([]discoveredPlaybookDTO, len(d.Playbooks))
	for i, p := range d.Playbooks {
		dto := discoveredPlaybookDTO{Path: p.Path, Fields: p.Fields, Warnings: p.Warnings, Failed: p.Failed, LeftOut: p.LeftOut}
		if p.PlaybookID != nil {
			dto.PlaybookID = p.PlaybookID.String()
		}
		out.Body.Playbooks[i] = dto
	}
	return out
}

// registerRepoPlaybookRoutes wires a repository's discovered playbooks
// (backlog 7.75): reading them needs repos.read, picking one (registering
// its path as a playbook, or finding the one registered) playbooks.write.
func registerRepoPlaybookRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/repos/{id}/playbooks", func(ctx context.Context, in *repoPlaybooksInput) (*repoPlaybooksOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermReposRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "repo id")
		if err != nil {
			return nil, err
		}
		d, err := deps.Playbooks.Discovered(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapPlaybooksErr(ctx, err)
		}
		return toRepoPlaybooksOutput(d), nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/repos/{id}/playbooks", func(ctx context.Context, in *repoPlaybookPickInput) (*repoPlaybookPickOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermPlaybooksWrite)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "repo id")
		if err != nil {
			return nil, err
		}
		p, created, err := deps.Playbooks.EnsureRepo(ctx, scope.OrgID, id, in.Body.Path)
		if err != nil {
			return nil, mapPlaybooksErr(ctx, err)
		}
		verb := "picked"
		if created {
			verb = "picked new"
		}
		setAuditObject(ctx, p.ID.String(), playbookSummary(p, verb))
		out := &repoPlaybookPickOutput{}
		out.Body.Playbook, out.Body.Created = toPlaybookDTO(p), created
		return out, nil
	}, withAudit("pickRepoPlaybook", "playbook"))
}
