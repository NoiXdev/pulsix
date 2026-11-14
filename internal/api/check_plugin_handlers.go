package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// GET /check-plugins (backlog 3.54, spec docs/superpowers/specs/2026-11-12-
// plugin-descriptors-design.md, "Manager"): every check plugin's
// descriptor, which the SPA builds its check forms and their client-side
// validation from. The data is static — compiled into the manager, the
// same for every organization — so the route takes no org and asks only
// for a signed-in, MFA-complete session, like every other route outside
// the org-scoped group (registerMyActivityRoute); a bearer token, scoped
// to one organization's routes, does not reach it.
//
// The DTOs below mirror checkplugins' JSON view field for field, so the
// body is exactly json.Marshal(checkplugins.All()) — the API test compares
// the two, so a field added to a descriptor and forgotten here fails
// there. They exist, rather than serving the descriptors as they are, to
// give the OpenAPI document closed enums the leaf package cannot declare
// (it imports nothing, huma included).

type checkPluginParamDTO struct {
	Name     string `json:"name"`
	Type     string `json:"type" enum:"string,integer,bool,enum,list"`
	Required bool   `json:"required,omitempty" doc:"Absent means the parameter is optional"`
	// Default is a string, an integer or a boolean, typed by
	// TransformSchema below.
	Default    any      `json:"default,omitempty" doc:"What the plugin applies when the parameter is absent; documentation only, the manager stores exactly what was sent"`
	Example    string   `json:"example,omitempty" doc:"A sample value for a parameter without a default"`
	Min        *int64   `json:"min,omitempty" doc:"An integer's lower bound, inclusive"`
	Max        *int64   `json:"max,omitempty" doc:"An integer's upper bound, inclusive"`
	MaxEntries int      `json:"max_entries,omitempty" doc:"A list's maximum length; absent means unbounded"`
	MaxBytes   int      `json:"max_bytes,omitempty" doc:"A string's, or each list entry's, maximum length in bytes; absent means unbounded"`
	NonEmpty   bool     `json:"non_empty,omitempty" doc:"The empty string, or the empty list, is refused"`
	Options    []string `json:"options,omitempty" nullable:"false" doc:"An enum's allowed values, in the order a form offers them"`
	Format     string   `json:"format,omitempty" doc:"A named validator a string, or each list entry, must also pass"`
}

// TransformSchema types default as the union its values come from: an
// `any` field would otherwise be an unconstrained schema.
func (checkPluginParamDTO) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	if p, ok := s.Properties["default"]; ok {
		p.OneOf = []*huma.Schema{
			{Type: huma.TypeString},
			{Type: huma.TypeInteger},
			{Type: huma.TypeBoolean},
		}
	}
	return s
}

type checkPluginMetricDTO struct {
	Name string `json:"name"`
	Kind string `json:"kind" enum:"numeric,discrete" doc:"numeric metrics carry a number a threshold compares against, discrete ones a state label"`
}

type checkPluginDTO struct {
	Name                checkPluginName        `json:"name"`
	Runs                string                 `json:"runs" enum:"agent,server" doc:"agent plugins run on the host's agent, server ones are probed from the manager"`
	Params              []checkPluginParamDTO  `json:"params" nullable:"false" doc:"In the order a form shows them; empty for a plugin that takes none"`
	ItemParam           string                 `json:"item_param,omitempty" doc:"The list parameter that bounds which items the plugin reports on, if any. An entry is not always an item's exact name: smart reports a controller disk as \"<path> [<type>]\" under its path's entry, script reports values as \"<command>:<label>\" under the command's, and snmp's oids entries name=OID report on item name, while its interfaces flag adds every interface the device names"`
	HostItemAlwaysAsked bool                   `json:"host_item_always_asked,omitempty" doc:"The plugin also reports on the whole-host item whatever its item list says"`
	Metrics             []checkPluginMetricDTO `json:"metrics" nullable:"false"`
}

func toCheckPluginDTO(d checkplugins.Descriptor) checkPluginDTO {
	params := make([]checkPluginParamDTO, len(d.Params))
	for i, p := range d.Params {
		params[i] = checkPluginParamDTO{
			Name: p.Name, Type: string(p.Type), Required: p.Required, Default: p.Default,
			Example: p.Example, Min: p.Min, Max: p.Max, MaxEntries: p.MaxEntries,
			MaxBytes: p.MaxBytes, NonEmpty: p.NonEmpty, Options: p.Options, Format: string(p.Format),
		}
	}
	metrics := make([]checkPluginMetricDTO, len(d.Metrics))
	for i, m := range d.Metrics {
		metrics[i] = checkPluginMetricDTO{Name: m.Name, Kind: string(m.Kind)}
	}
	return checkPluginDTO{
		Name: checkPluginName(d.Name), Runs: string(d.Runs), Params: params,
		ItemParam: d.ItemParam, HostItemAlwaysAsked: d.HostItemAlwaysAsked, Metrics: metrics,
	}
}

type checkPluginListOutput struct {
	Body struct {
		Plugins []checkPluginDTO `json:"plugins" nullable:"false" doc:"Every check plugin, in the order a form offers them"`
	}
}

func registerCheckPluginRoutes(hapi huma.API) {
	huma.Get(hapi, "/api/v1/check-plugins", func(ctx context.Context, _ *struct{}) (*checkPluginListOutput, error) {
		if _, err := requireMFAComplete(ctx); err != nil {
			return nil, err
		}
		all := checkplugins.All()
		out := &checkPluginListOutput{}
		out.Body.Plugins = make([]checkPluginDTO, len(all))
		for i, d := range all {
			out.Body.Plugins[i] = toCheckPluginDTO(d)
		}
		return out, nil
	})
}
