package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type playbookOutlineInput struct {
	Org  string `path:"org"`
	Body struct {
		Content string `json:"content" doc:"The playbook YAML text, e.g. the editor's current buffer"`
	}
}

type playbookOutlineOutput struct {
	Body playbooks.PlaybookOutline
}

// registerPlaybookOutlineRoutes wires the stateless outline of a playbook
// text. It reads nothing from the org's data, but is gated like the other
// playbook reads.
func registerPlaybookOutlineRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs/{org}/playbooks/outline", func(ctx context.Context, in *playbookOutlineInput) (*playbookOutlineOutput, error) {
		if _, err := requireOrg(ctx, deps, in.Org, tenancy.PermPlaybooksRead); err != nil {
			return nil, err
		}
		o, err := playbooks.Outline([]byte(in.Body.Content))
		if err != nil {
			if errors.Is(err, playbooks.ErrInvalidInput) {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			return nil, huma.Error422UnprocessableEntity("playbook cannot be read")
		}
		return &playbookOutlineOutput{Body: o}, nil
	})
}
