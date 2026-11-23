package api

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type playbookContentOutput struct {
	Body struct {
		Path            string    `json:"path" doc:"The playbook's path inside its repository"`
		Commit          string    `json:"commit" doc:"The commit the last sync read; empty when the text is from an earlier sync (see from_earlier_sync)"`
		FromEarlierSync bool      `json:"from_earlier_sync" doc:"True when a limit left the playbook unread in the last sync and the text is from an earlier one"`
		SyncedAt        time.Time `json:"synced_at" doc:"When the last sync recorded the repository"`
		Content         *string   `json:"content,omitempty" doc:"The playbook file's text as of the last sync; absent when the sync stored none"`
		LeftOut         string    `json:"left_out,omitempty" enum:"too_large,budget,not_utf8,unreadable,not_sent" doc:"Why there is no text: the file is over 512 KiB (too_large), the sync's texts were full (budget), it is not UTF-8 text (not_utf8), it could not be read, or a limit left the playbook unread and no earlier text exists (unreadable), or the last sync sent none or did not report the file (not_sent)"`
	}
}

// registerPlaybookContentRoutes wires a repo playbook's text as of its
// repository's last sync (backlog 3.127), gated like the other playbook
// reads. An editor playbook's text is its DTO's inline document (422
// here); a repository never synced is 404, a path its last sync did not
// report has no text (not_sent).
func registerPlaybookContentRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/playbooks/{id}/content", func(ctx context.Context, in *playbookGetInput) (*playbookContentOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermPlaybooksRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "playbook id")
		if err != nil {
			return nil, err
		}
		text, err := deps.Playbooks.RepoText(ctx, scope.OrgID, id)
		switch {
		case errors.Is(err, playbooks.ErrNotRepo):
			return nil, huma.Error422UnprocessableEntity("an editor playbook's text is in the playbook itself")
		case errors.Is(err, playbooks.ErrNoRepoText):
			return nil, huma.Error404NotFound("the repository has not been synced yet")
		case err != nil:
			return nil, mapPlaybooksErr(ctx, err)
		}
		out := &playbookContentOutput{}
		out.Body.Path, out.Body.Commit, out.Body.SyncedAt = text.Path, text.Commit, text.SyncedAt
		out.Body.Content, out.Body.LeftOut = text.Content, text.LeftOut
		out.Body.FromEarlierSync = text.FromEarlierSync
		return out, nil
	})
}
