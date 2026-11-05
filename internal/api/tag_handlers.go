package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/hosts/selector"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// tagDTO is the wire shape of one tag, carrying its source so a client can
// tell a person-set value from one derived from agent facts (and, in a
// PUT /hosts/{id}/tags editor, gray out or hide the fact rows it cannot
// change).
type tagDTO struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

func toTagDTO(t hosts.Tag) tagDTO {
	return tagDTO{Key: t.Key, Value: t.Value, Source: string(t.Source)}
}

type hostTagsGetInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type hostTagsOutput struct {
	Body struct {
		Tags []tagDTO `json:"tags"`
	}
}

// tagInput is one tag as PUT /hosts/{id}/tags accepts it: key and value
// only — the caller never names a source, since this route can only ever
// set manual tags (see hosts.Service.SetTags' own doc comment). The size
// limits mirror hosts.tagKeyPattern/maxTagValueLen so a request this
// obviously oversized is rejected by huma's own validation before it ever
// reaches the service.
type tagInput struct {
	Key   string `json:"key" minLength:"1" maxLength:"64"`
	Value string `json:"value" maxLength:"256"`
}

// tagsFromBody turns a request's tags into the manual tags the hosts service
// stores; the wire format has no source, a person always sets manual ones.
func tagsFromBody(body []tagInput) []hosts.Tag {
	tags := make([]hosts.Tag, len(body))
	for i, tg := range body {
		tags[i] = hosts.Tag{Key: tg.Key, Value: tg.Value}
	}
	return hosts.ManualTags(tags)
}

type hostTagsSetInput struct {
	Org     string `path:"org"`
	ID      string `path:"id"`
	IfMatch string `header:"If-Match" doc:"The host's ETag; optional — when sent, a stale one answers 412"`
	Body    []tagInput
}

// hostTagsPutOutput is the PUT answer: the tags as GET returns them, plus
// the host's new revision (also as ETag), so a client holding the host open
// stays current without a re-read (backlog 7.79).
type hostTagsPutOutput struct {
	ETag string `header:"ETag"`
	Body struct {
		Tags         []tagDTO `json:"tags"`
		HostRevision int64    `json:"host_revision" doc:"The host's revision after this write; the ETag carries the same value"`
	}
}

// orgTagKeysInput has no query parameters — GET /orgs/{org}/tags always
// returns every key currently in use across the organization; a later
// milestone can add filtering if the autocomplete list ever needs it.
type orgTagKeysInput struct {
	Org string `path:"org"`
}

type valueCountDTO struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type keyValuesDTO struct {
	Key    string          `json:"key"`
	Values []valueCountDTO `json:"values"`
}

func toKeyValuesDTO(kv hosts.KeyValues) keyValuesDTO {
	out := keyValuesDTO{Key: kv.Key, Values: make([]valueCountDTO, len(kv.Values))}
	for i, v := range kv.Values {
		out.Values[i] = valueCountDTO{Value: v.Value, Count: v.Count}
	}
	return out
}

type orgTagKeysOutput struct {
	Body struct {
		Keys []keyValuesDTO `json:"keys"`
	}
}

type hostQueryInput struct {
	Org  string `path:"org"`
	Body struct {
		Selector string `json:"selector" minLength:"1"`
	}
}

type hostQueryOutput struct {
	Body struct {
		Hosts []hostDTO `json:"hosts"`
	}
}

// registerTagRoutes wires host tags and the tag selector:
// GET/PUT /orgs/{org}/hosts/{id}/tags, GET /orgs/{org}/tags, and
// POST /orgs/{org}/hosts/query. It is mounted on the same bearer-enabled
// *huma.Group registerHostRoutes uses (see registerRoutes, api.go), for the
// same reason: every route here resolves its scope through requireOrg.
func registerTagRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}/tags", func(ctx context.Context, in *hostTagsGetInput) (*hostTagsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsRead)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid host id")
		}
		tags, err := deps.Hosts.ListTags(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		out := &hostTagsOutput{}
		out.Body.Tags = make([]tagDTO, len(tags))
		for i, tg := range tags {
			out.Body.Tags[i] = toTagDTO(tg)
		}
		return out, nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/hosts/{id}/tags", func(ctx context.Context, in *hostTagsSetInput) (*hostTagsPutOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsWrite)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid host id")
		}
		ifRev, err := optionalIfMatch(in.IfMatch)
		if err != nil {
			return nil, err
		}
		rev, err := deps.Hosts.SetTags(ctx, scope.OrgID, id, tagsFromBody(in.Body), ifRev)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "set tags for host")

		all, err := deps.Hosts.ListTags(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		out := &hostTagsPutOutput{ETag: revisionETag(rev)}
		out.Body.HostRevision = rev
		out.Body.Tags = make([]tagDTO, len(all))
		for i, tg := range all {
			out.Body.Tags[i] = toTagDTO(tg)
		}
		return out, nil
	}, withAudit("setHostTags", "host"))

	huma.Get(hapi, "/api/v1/orgs/{org}/tags", func(ctx context.Context, in *orgTagKeysInput) (*orgTagKeysOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsRead)
		if err != nil {
			return nil, err
		}
		keys, err := deps.Hosts.ListTagKeys(ctx, scope.OrgID)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		out := &orgTagKeysOutput{}
		out.Body.Keys = make([]keyValuesDTO, len(keys))
		for i, kv := range keys {
			out.Body.Keys[i] = toKeyValuesDTO(kv)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/hosts/query", func(ctx context.Context, in *hostQueryInput) (*hostQueryOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermHostsRead)
		if err != nil {
			return nil, err
		}
		sel, err := selector.Parse(in.Body.Selector)
		if err != nil {
			// A syntax error in the caller's own selector is their mistake,
			// not ours — reported as a client error carrying the parser's
			// own message (which names where the input went wrong) rather
			// than falling through to a generic 500. 422, not 400: the
			// request body is well-formed JSON with a syntactically valid
			// field, it is the *selector expression inside it* that fails to
			// parse — the same distinction huma's own validation failures
			// use 422 for.
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		list, err := deps.Hosts.QueryHosts(ctx, scope.OrgID, sel)
		if err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		out := &hostQueryOutput{}
		out.Body.Hosts = make([]hostDTO, len(list))
		for i, h := range list {
			out.Body.Hosts[i] = toHostDTO(h)
		}
		return out, nil
	}) // no withAudit: this is a read (a POST only because the filter is a
	// request body, not query params) — see auditAllowlist's entry for this
	// exact route (audit_coverage_test.go) for the full reasoning.
}
