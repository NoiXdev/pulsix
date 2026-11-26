package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The read tools (spec 2026-11-16 assistant access, "Tools"): each one is a
// GET of the REST route it names, its body passed through as the result,
// so it shows exactly what the route shows a person with the same rights.

type pageInput struct {
	Cursor string `json:"cursor,omitempty" jsonschema:"the next_cursor of the previous page"`
	Limit  int    `json:"limit,omitempty" jsonschema:"page size (1 to 100)"`
}

func (p pageInput) query() url.Values {
	q := url.Values{}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	return q
}

type hostListInput struct {
	pageInput
	Query   string `json:"q,omitempty" jsonschema:"filter by name or address"`
	ZoneID  string `json:"zone_id,omitempty"`
	Managed *bool  `json:"managed,omitempty" jsonschema:"only managed (Ansible) hosts, or only unmanaged ones"`
}

type hostInput struct {
	HostID string `json:"host_id" jsonschema:"the host's id"`
}

type groupInput struct {
	GroupID string `json:"group_id" jsonschema:"the group's id"`
}

type previewInput struct {
	GroupID string `json:"group_id,omitempty" jsonschema:"render only this group and its descendants"`
}

type repoInput struct {
	RepoID string `json:"repo_id" jsonschema:"the repository's id"`
}

type playbookInput struct {
	PlaybookID string `json:"playbook_id" jsonschema:"the playbook's id"`
}

type templateInput struct {
	TemplateID string `json:"template_id" jsonschema:"the job template's id"`
}

// passGet returns the GET body of an org route as the tool result.
func passGet(ctx context.Context, c *caller, query url.Values, format string, ids ...string) (any, error) {
	path, err := c.orgPath(format, ids...)
	if err != nil {
		return nil, err
	}
	res, err := c.get(ctx, path, query, nil)
	if err != nil {
		return nil, err
	}
	return withoutSchemaLink(res.Body), nil
}

func readTools() []toolDef {
	return []toolDef{
		define("list_hosts", "List hosts", "Hosts of the organization, a page at a time.",
			tenancy.AreaInventoryRead, readTool, func(ctx context.Context, c *caller, in hostListInput) (any, error) {
				q := in.query()
				if in.Query != "" {
					q.Set("q", in.Query)
				}
				if in.ZoneID != "" {
					q.Set("zone", in.ZoneID)
				}
				if in.Managed != nil {
					q.Set("managed", strconv.FormatBool(*in.Managed))
				}
				return passGet(ctx, c, q, "/hosts")
			}),
		define("get_host_vars", "Get host variables", "The effective variables of a host as the inventory renders them: the values of the groups it is in and of their parent groups (a deeper group wins), the values it has in its memberships, and its own (address and SSH settings as ansible_ variables, facts). sources names, per variable, the group the value comes from, or \"host\" for the host's own. A secret shows as {\"$secret\": \"set\"}, never its value. assistant_written names the variables whose value an assistant wrote: a run receives them as plain text ({\"__ansible_unsafe\": …}, never templated) until a person saves them. Change a host's values in a group with set_member_vars, a group's own with set_group_vars.",
			tenancy.AreaInventoryRead, readTool, func(ctx context.Context, c *caller, in hostInput) (any, error) {
				path, err := c.orgPath("/hosts/%s/inventory/preview", in.HostID)
				if err != nil {
					return nil, err
				}
				res, err := c.get(ctx, path, nil, nil)
				if err != nil {
					return nil, err
				}
				dec := json.NewDecoder(bytes.NewReader(res.Body))
				dec.UseNumber()
				var body map[string]any
				if err := dec.Decode(&body); err != nil {
					return nil, fmt.Errorf("decode response: %w", err)
				}
				delete(body, "$schema")
				body["vars"] = unmaskSecrets(body["vars"])
				return body, nil
			}),
		define("get_host", "Get host", "One host with its SSH settings (credential by reference), its tags and the inventory groups it belongs to.",
			tenancy.AreaInventoryRead, readTool, func(ctx context.Context, c *caller, in hostInput) (any, error) {
				host, err := passGet(ctx, c, nil, "/hosts/%s", in.HostID)
				if err != nil {
					return nil, err
				}
				ssh, err := passGet(ctx, c, nil, "/hosts/%s/ssh", in.HostID)
				if err != nil {
					return nil, err
				}
				groups, err := passGet(ctx, c, nil, "/hosts/%s/inventory/groups", in.HostID)
				if err != nil {
					return nil, err
				}
				tags, err := passGet(ctx, c, nil, "/hosts/%s/tags", in.HostID)
				if err != nil {
					return nil, err
				}
				return map[string]any{"host": host, "ssh": ssh, "groups": groups, "tags": tags}, nil
			}),
		define("list_groups", "List groups", "The inventory groups, with their parents.",
			tenancy.AreaInventoryRead, readTool, func(ctx context.Context, c *caller, _ noInput) (any, error) {
				return passGet(ctx, c, nil, "/inventory/groups")
			}),
		define("get_group", "Get group", "One inventory group: description, parent, variable schema, variables (secret values read as {\"$secret\":\"set\"}) and its members with their membership variables. assistant_keys (group and member) and assistant_default_keys name the values and schema defaults an assistant wrote: a run receives them as plain text until a person saves them.",
			tenancy.AreaInventoryRead, readTool, func(ctx context.Context, c *caller, in groupInput) (any, error) {
				group, err := passGet(ctx, c, nil, "/inventory/groups/%s", in.GroupID)
				if err != nil {
					return nil, err
				}
				members, err := passGet(ctx, c, nil, "/inventory/groups/%s/members", in.GroupID)
				if err != nil {
					return nil, err
				}
				return map[string]any{"group": group, "members": members}, nil
			}),
		define("preview_inventory", "Preview inventory", "The inventory as a playbook run sees it (secret values masked). Values an assistant wrote appear as {\"__ansible_unsafe\": …} (plain text, never templated) and are listed in assistant_written until a person saves them.",
			tenancy.AreaInventoryRead, readTool, func(ctx context.Context, c *caller, in previewInput) (any, error) {
				q := url.Values{}
				if in.GroupID != "" {
					q.Set("group", in.GroupID)
				}
				return passGet(ctx, c, q, "/inventory/preview")
			}),
		define("list_repos", "List repositories", "The git repositories, with URL, branch, subpath and last sync.",
			tenancy.AreaReposRead, readTool, func(ctx context.Context, c *caller, _ noInput) (any, error) {
				return passGet(ctx, c, nil, "/repos")
			}),
		define("get_repo", "Get repository", "One git repository, with last_synced_commit (the commit of the last successful sync, absent before one), last_synced_at (when the sync that fetched it ran, when known) and last_synced_commit_current: false, with last_synced_commit_stale saying why, when a run cannot be pinned to that commit (repo_changed: the URL, branch or subpath changed since; not_reported: the sync came from a worker that reports no sources; sync_unrecorded), next to last_sync.",
			tenancy.AreaReposRead, readTool, func(ctx context.Context, c *caller, in repoInput) (any, error) {
				return passGet(ctx, c, nil, "/repos/%s", in.RepoID)
			}),
		define("list_repo_files", "List repository playbooks", "The playbook files found in a repository at its last sync.",
			tenancy.AreaReposRead, readTool, func(ctx context.Context, c *caller, in repoInput) (any, error) {
				return passGet(ctx, c, nil, "/repos/%s/playbooks", in.RepoID)
			}),
		define("list_playbooks", "List playbooks", "The playbooks job templates can use.",
			tenancy.AreaReposRead, readTool, func(ctx context.Context, c *caller, _ noInput) (any, error) {
				return passGet(ctx, c, nil, "/playbooks")
			}),
		define("get_playbook", "Get playbook", "One playbook.",
			tenancy.AreaReposRead, readTool, func(ctx context.Context, c *caller, in playbookInput) (any, error) {
				return passGet(ctx, c, nil, "/playbooks/%s", in.PlaybookID)
			}),
		define("list_job_templates", "List job templates", "The job templates.",
			tenancy.AreaTemplatesRead, readTool, func(ctx context.Context, c *caller, _ noInput) (any, error) {
				return passGet(ctx, c, nil, "/job-templates")
			}),
		define("get_job_template", "Get job template", "One job template: playbook, scope, limit, extra vars (secret values read as {\"$secret\":\"set\"}), SSH credential and vault identities by reference, and its revision.",
			tenancy.AreaTemplatesRead, readTool, func(ctx context.Context, c *caller, in templateInput) (any, error) {
				return passGet(ctx, c, nil, "/job-templates/%s", in.TemplateID)
			}),
		define("list_credentials", "List credentials", "Credentials by id, name and kind only, to reference them from repositories, hosts and job templates. Their contents are never shown.",
			tenancy.AreaCredentialsNames, readTool, func(ctx context.Context, c *caller, _ noInput) (any, error) {
				path, err := c.orgPath("/credentials")
				if err != nil {
					return nil, err
				}
				var body struct {
					Credentials []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
						Kind string `json:"kind"`
					} `json:"credentials"`
				}
				if _, err := c.get(ctx, path, nil, &body); err != nil {
					return nil, err
				}
				if body.Credentials == nil {
					body.Credentials = []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
						Kind string `json:"kind"`
					}{}
				}
				return map[string]any{"credentials": body.Credentials}, nil
			}),
	}
}

// unmaskSecrets turns the preview's mask into the {"$secret":"set"} form
// every other read of the assistant surface uses.
func unmaskSecrets(v any) any {
	switch t := v.(type) {
	case string:
		if t == render.MaskedSecret {
			return map[string]any{"$secret": "set"}
		}
	case map[string]any:
		for k, e := range t {
			t[k] = unmaskSecrets(e)
		}
	case []any:
		for i, e := range t {
			t[i] = unmaskSecrets(e)
		}
	}
	return v
}
