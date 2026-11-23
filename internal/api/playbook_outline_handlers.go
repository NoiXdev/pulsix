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
	Body PlaybookOutlineDTO
}

// The outline's wire types are the API's own, so the published schema names
// are API names (…DTO), not the domain package's; the JSON shape is the
// domain's.

// PlaybookOutlineDTO is the structure of one playbook document.
type PlaybookOutlineDTO struct {
	Plays     []PlaybookOutlinePlayDTO `json:"plays" nullable:"false"`
	Tags      []string                 `json:"tags" nullable:"false" doc:"Every tag the outline carries, sorted"`
	Counts    PlaybookOutlineCountsDTO `json:"counts"`
	Truncated bool                     `json:"truncated" doc:"True when a cap cut the outline short: entries, depth, tags or text were left out or clipped"`
}

// PlaybookOutlinePlayDTO is one play, or a top-level import_playbook.
type PlaybookOutlinePlayDTO struct {
	Name    string                    `json:"name"`
	Hosts   string                    `json:"hosts"`
	Become  bool                      `json:"become"`
	Line    int                       `json:"line"`
	Tags    []string                  `json:"tags" nullable:"false"`
	Entries []PlaybookOutlineEntryDTO `json:"entries" nullable:"false"`
}

// PlaybookOutlineEntryDTO is one task, block, include/import or role.
type PlaybookOutlineEntryDTO struct {
	Section         string   `json:"section"`
	Kind            string   `json:"kind"`
	Name            string   `json:"name" doc:"Empty when the entry has no usable name; show a word for its kind instead"`
	Line            int      `json:"line"`
	Depth           int      `json:"depth"`
	Tags            []string `json:"tags" nullable:"false"`
	RunsInCheckMode bool     `json:"runs_in_check_mode" doc:"False when the entry may run for real in a dry run"`
}

// PlaybookOutlineCountsDTO summarises an outline.
type PlaybookOutlineCountsDTO struct {
	Plays    int `json:"plays"`
	Tasks    int `json:"tasks"`
	Handlers int `json:"handlers"`
}

func toPlaybookOutlineDTO(o playbooks.PlaybookOutline) PlaybookOutlineDTO {
	plays := make([]PlaybookOutlinePlayDTO, 0, len(o.Plays))
	for _, p := range o.Plays {
		entries := make([]PlaybookOutlineEntryDTO, 0, len(p.Entries))
		for _, e := range p.Entries {
			entries = append(entries, PlaybookOutlineEntryDTO{
				Section: e.Section, Kind: e.Kind, Name: e.Name, Line: e.Line, Depth: e.Depth,
				Tags: nonNilStrings(e.Tags), RunsInCheckMode: e.RunsInCheckMode,
			})
		}
		plays = append(plays, PlaybookOutlinePlayDTO{
			Name: p.Name, Hosts: p.Hosts, Become: p.Become, Line: p.Line, Tags: nonNilStrings(p.Tags), Entries: entries,
		})
	}
	return PlaybookOutlineDTO{
		Plays: plays, Tags: nonNilStrings(o.Tags), Truncated: o.Truncated,
		Counts: PlaybookOutlineCountsDTO{Plays: o.Counts.Plays, Tasks: o.Counts.Tasks, Handlers: o.Counts.Handlers},
	}
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
		return &playbookOutlineOutput{Body: toPlaybookOutlineDTO(o)}, nil
	})
}
