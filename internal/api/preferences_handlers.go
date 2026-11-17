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
		out.Body.Lists[id] = listPrefBody{Columns: cols}
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
// the change is audited on the user's own trail without any value.
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
			prefs.Lists[id] = preferences.List{Columns: l.Columns}
		}
		stored, err := deps.Preferences.Set(ctx, p.UserID, prefs)
		if err != nil {
			return nil, mapPreferencesErr(ctx, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, p.UserID.String(), "changed their appearance settings")
		return toPreferencesOutput(stored), nil
	}, withAudit("setPreferences", "user"))
}
