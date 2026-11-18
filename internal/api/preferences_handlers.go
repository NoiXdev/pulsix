package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/preferences"
)

// listPrefBody is one list's chosen columns.
type listPrefBody struct {
	Columns []string `json:"columns" maxItems:"40" doc:"Column ids in display order."`
	Known   []string `json:"known,omitempty" maxItems:"80" doc:"Every column id the list offered when the choice was saved; a column missing here is new since. Absent in older data: then equal to columns."`
}

// preferencesBody is the whole preferences document of the signed-in user.
type preferencesBody struct {
	Density  string                  `json:"density" enum:"comfortable,standard,compact"`
	PageSize int                     `json:"page_size" enum:"10,25,50,100"`
	Lists    map[string]listPrefBody `json:"lists" doc:"Per list id, the chosen columns."`
}

type preferencesOutput struct{ Body preferencesBody }

type setPreferencesInput struct{ Body preferencesBody }

func toPreferencesOutput(p preferences.Prefs) *preferencesOutput {
	out := &preferencesOutput{}
	out.Body.Density = string(p.Density)
	out.Body.PageSize = p.PageSize
	out.Body.Lists = make(map[string]listPrefBody, len(p.Lists))
	for id, l := range p.Lists {
		cols := l.Columns
		if cols == nil {
			cols = []string{}
		}
		out.Body.Lists[id] = listPrefBody{Columns: cols, Known: l.Known}
	}
	return out
}

func mapPreferencesErr(ctx context.Context, err error) error {
	if errors.Is(err, preferences.ErrInvalid) {
		return huma.Error422UnprocessableEntity(err.Error())
	}
	return mapAuthErr(ctx, err)
}

// registerPreferencesRoutes wires the signed-in user's own appearance
// settings (backlog 3.97–3.100). Like the language it is an account setting
// of the person at the browser: an interactive session, never a token, and
// the change is not audited: it is cosmetic and per person, and a save per
// column toggle would bury the useful entries of My activity (backlog 3.105).
func registerPreferencesRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/auth/me/preferences", func(ctx context.Context, _ *struct{}) (*preferencesOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		prefs, err := deps.Preferences.Get(ctx, p.UserID)
		if err != nil {
			return nil, mapPreferencesErr(ctx, err)
		}
		return toPreferencesOutput(prefs), nil
	})
	// Not audited (backlog 3.105); the operation keeps the name the audit
	// wrapper gave it, which the generated SPA client keys its types by.
	huma.Put(hapi, "/api/v1/auth/me/preferences", func(ctx context.Context, in *setPreferencesInput) (*preferencesOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		prefs := preferences.Prefs{
			Density: preferences.Density(in.Body.Density), PageSize: in.Body.PageSize,
			Lists: make(map[string]preferences.List, len(in.Body.Lists)),
		}
		for id, l := range in.Body.Lists {
			prefs.Lists[id] = preferences.List{Columns: l.Columns, Known: l.Known}
		}
		stored, err := deps.Preferences.Set(ctx, p.UserID, prefs)
		if err != nil {
			return nil, mapPreferencesErr(ctx, err)
		}
		return toPreferencesOutput(stored), nil
	}, func(o *huma.Operation) { o.OperationID = "setPreferences" })
}
