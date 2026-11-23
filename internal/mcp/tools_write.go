package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The write tools (spec 2026-11-16 assistant access, "Tools"). None
// deletes anything but a group membership. Updates read the object first,
// overlay the fields the caller names, and send the revision they read (or
// the one the caller passes) as If-Match, so a concurrent change is
// refused, never overwritten. Every vars write passes the secret rules.

// Body keys each update route takes, which are also the keys of the object
// its GET returns.
var (
	hostKeys     = []string{"name", "zone_id", "address", "managed", "monitored", "kind"}
	groupKeys    = []string{"name", "description", "parent_id", "bastion_id"}
	repoKeys     = []string{"name", "url", "branch", "subpath", "credential_id", "vault_credential_id", "vault_identities"}
	templateKeys = []string{"name", "playbook_id", "scope", "limit", "extra_vars", "extra_vars_schema", "credential_id", "vault_identities"}
)

type hostCreateArgs struct {
	Name      string `json:"name" jsonschema:"host name (letters, digits, dots, dashes)"`
	Address   string `json:"address,omitempty" jsonschema:"IP address or DNS name"`
	ZoneID    string `json:"zone_id" jsonschema:"the zone the host is reached from (list_zones)"`
	Managed   bool   `json:"managed,omitempty" jsonschema:"whether playbooks run against the host"`
	Monitored bool   `json:"monitored,omitempty" jsonschema:"whether an agent monitors the host"`
	Kind      string `json:"kind,omitempty" jsonschema:"server (default) or client"`
}

// schemaTypeWords says what a variable schema's fields are. The tools that
// take a schema describe it in these words: a template's extra_vars_schema
// as schemaFieldWords, a group's as groupSchemaFieldWords, which adds the
// scope only a group field has. The argument tags below repeat them
// because a struct tag cannot hold a constant.
const (
	schemaTypeWords       = "fields with name, type (string, int, float, bool, enum, secret, list, repeater, object)"
	schemaFieldWords      = schemaTypeWords + " and more"
	groupSchemaFieldWords = schemaTypeWords + ", scope (group or host) and more"
)

type updateArgs struct {
	Revision *int64         `json:"revision,omitempty" jsonschema:"the revision you read; omitted means the current one"`
	Fields   map[string]any `json:"fields" jsonschema:"the fields to change; the others keep their current value"`
}

type hostUpdateArgs struct {
	HostID string `json:"host_id"`
	updateArgs
}

type hostSSHArgs struct {
	HostID       string  `json:"host_id"`
	Username     string  `json:"username,omitempty" jsonschema:"SSH user"`
	Port         int     `json:"port,omitempty" jsonschema:"SSH port; 0 or omitted means 22"`
	CredentialID *string `json:"credential_id,omitempty" jsonschema:"SSH credential (list_credentials); omitted uses the job template's"`
	BastionMode  string  `json:"bastion_mode,omitempty" jsonschema:"inherit (default), none or explicit"`
	BastionID    *string `json:"bastion_id,omitempty" jsonschema:"the jump host when bastion_mode is explicit"`
}

type groupCreateArgs struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	ParentID    *string `json:"parent_id,omitempty" jsonschema:"the parent group; omitted makes a root group"`
}

type groupUpdateArgs struct {
	GroupID string `json:"group_id"`
	updateArgs
}

type groupSchemaArgs struct {
	GroupID  string         `json:"group_id"`
	Revision *int64         `json:"revision,omitempty"`
	Schema   map[string]any `json:"schema" jsonschema:"the group's variable schema: fields with name, type (string, int, float, bool, enum, secret, list, repeater, object), scope (group or host) and more"`
}

type groupVarsArgs struct {
	GroupID  string         `json:"group_id"`
	Revision *int64         `json:"revision,omitempty"`
	Vars     map[string]any `json:"vars" jsonschema:"the group's values; only keys its schema declares. A secret field takes only {\"$secret\": \"keep\"}"`
}

type memberArgs struct {
	GroupID string         `json:"group_id"`
	HostID  string         `json:"host_id"`
	Vars    map[string]any `json:"vars,omitempty" jsonschema:"host-scoped values and overrides; a secret field takes only {\"$secret\": \"keep\"}"`
}

type memberRemoveArgs struct {
	GroupID string `json:"group_id"`
	HostID  string `json:"host_id"`
}

type vaultIdentity struct {
	Label        string `json:"label"`
	CredentialID string `json:"credential_id" jsonschema:"a vault_password credential (list_credentials)"`
}

type repoCreateArgs struct {
	Name            string          `json:"name"`
	URL             string          `json:"url" jsonschema:"clone URL; credentials never go in the URL"`
	Branch          string          `json:"branch,omitempty" jsonschema:"default main"`
	Subpath         string          `json:"subpath,omitempty" jsonschema:"the directory inside the repository that holds the playbooks"`
	CredentialID    string          `json:"credential_id,omitempty" jsonschema:"git credential (list_credentials)"`
	VaultIdentities []vaultIdentity `json:"vault_identities,omitempty"`
}

type repoUpdateArgs struct {
	RepoID string         `json:"repo_id"`
	Fields map[string]any `json:"fields" jsonschema:"the fields to change; the others keep their current value"`
}

type playbookCreateArgs struct {
	RepoID string `json:"repo_id"`
	Path   string `json:"path" jsonschema:"the playbook file, relative to the repository's subpath"`
	Name   string `json:"name,omitempty"`
}

type templateScope struct {
	GroupID string   `json:"group_id,omitempty"`
	HostIDs []string `json:"host_ids,omitempty"`
}

type templateCreateArgs struct {
	Name            string          `json:"name"`
	PlaybookID      string          `json:"playbook_id"`
	Scope           *templateScope  `json:"scope,omitempty" jsonschema:"the run's targets: a group, hosts or both; empty means every managed host"`
	Limit           string          `json:"limit,omitempty" jsonschema:"an Ansible limit pattern"`
	ExtraVars       map[string]any  `json:"extra_vars,omitempty"`
	ExtraVarsSchema map[string]any  `json:"extra_vars_schema,omitempty" jsonschema:"the typed schema the extra vars must satisfy, as {\"fields\": [...]}: fields with name, type (string, int, float, bool, enum, secret, list, repeater, object) and more"`
	CredentialID    string          `json:"credential_id,omitempty" jsonschema:"the default SSH credential"`
	VaultIdentities []vaultIdentity `json:"vault_identities,omitempty" jsonschema:"added vault passwords by label, e.g. noidee"`
}

type templateUpdateArgs struct {
	TemplateID string `json:"template_id"`
	updateArgs
}

func writeTools() []toolDef {
	return []toolDef{
		define("list_zones", "List zones", "The zones hosts are reached from.",
			tenancy.AreaInventoryRead, readTool, func(ctx context.Context, c *caller, _ noInput) (any, error) {
				return passGet(ctx, c, nil, "/zones")
			}),
		define("create_host", "Create host", "Add a host to the inventory.",
			tenancy.AreaInventoryWrite, writeTool, func(ctx context.Context, c *caller, in hostCreateArgs) (any, error) {
				return send(ctx, c, http.MethodPost, in, "", "/hosts")
			}),
		define("update_host", "Update host", "Change a host's name, address, zone, managed, monitored or kind. Fields: "+strings.Join(hostKeys, ", ")+".",
			tenancy.AreaInventoryWrite, writeTool, func(ctx context.Context, c *caller, in hostUpdateArgs) (any, error) {
				return update(ctx, c, http.MethodPatch, "host", hostKeys, in.updateArgs, nil, true, "/hosts/%s", in.HostID)
			}),
		define("set_host_ssh", "Set host SSH", "Set how the host is reached: user, port, SSH credential (by reference) and jump host.",
			tenancy.AreaInventoryWrite, writeTool, func(ctx context.Context, c *caller, in hostSSHArgs) (any, error) {
				body := map[string]any{"username": in.Username, "port": in.Port, "bastion_mode": in.BastionMode}
				if in.CredentialID != nil {
					body["credential_id"] = *in.CredentialID
				}
				if in.BastionID != nil {
					body["bastion_id"] = *in.BastionID
				}
				if in.BastionMode == "" {
					delete(body, "bastion_mode")
				}
				return send(ctx, c, http.MethodPut, body, "", "/hosts/%s/ssh", in.HostID)
			}),
		define("create_group", "Create group", "Add an inventory group, optionally below a parent.",
			tenancy.AreaInventoryWrite, writeTool, func(ctx context.Context, c *caller, in groupCreateArgs) (any, error) {
				return send(ctx, c, http.MethodPost, in, "", "/inventory/groups")
			}),
		define("update_group", "Update group", "Change a group's name, description, parent or jump host. Fields: "+strings.Join(groupKeys, ", ")+".",
			tenancy.AreaInventoryWrite, writeTool, func(ctx context.Context, c *caller, in groupUpdateArgs) (any, error) {
				return update(ctx, c, http.MethodPatch, "group", groupKeys, in.updateArgs, nil, true, "/inventory/groups/%s", in.GroupID)
			}),
		define("set_group_schema", "Set group schema", "Replace a group's variable schema ("+groupSchemaFieldWords+"). Variables need a schema field first. Fields may be added and marked secret while they hold no value (a person fills them in); a secret field keeps its type.",
			tenancy.AreaInventoryWrite, writeTool, func(ctx context.Context, c *caller, in groupSchemaArgs) (any, error) {
				cur, err := getObject(ctx, c, "group", "/inventory/groups/%s", in.GroupID)
				if err != nil {
					return nil, err
				}
				old, err := schemaOf(cur["schema"])
				if err != nil {
					return nil, err
				}
				updated, err := schemaOf(in.Schema)
				if err != nil {
					return nil, err
				}
				if err := checkSchemaChange(old, updated); err != nil {
					return nil, err
				}
				members, err := groupMembers(ctx, c, in.GroupID)
				if err != nil {
					return nil, err
				}
				stored := []map[string]any{asObject(cur["vars"])}
				for _, m := range members {
					stored = append(stored, m.Vars)
				}
				if err := checkNewSecretsEmpty(old, updated, stored...); err != nil {
					return nil, err
				}
				res, err := send(ctx, c, http.MethodPut, map[string]any{"schema": in.Schema}, ifMatch(in.Revision, cur), "/inventory/groups/%s/schema", in.GroupID)
				if err != nil {
					return nil, err
				}
				// Group vars are serialized with the schema by the group's
				// revision; a membership is not. One written between the
				// check above and the change holds a plain value under a
				// field that is secret now: it is taken out again.
				if fresh := newSecretPaths(old, updated); len(fresh) > 0 {
					members, err := groupMembers(ctx, c, in.GroupID)
					if err != nil {
						return nil, fmt.Errorf("check the memberships after the schema change: %w", err)
					}
					for _, m := range members {
						if err := clearNewSecrets(ctx, c, in.GroupID, m.HostID, m.Vars, updated, fresh); err != nil {
							return nil, err
						}
					}
				}
				return res, nil
			}),
		define("set_group_vars", "Set group variables", "Replace a group's values (only keys its schema declares). Secret fields take only {\"$secret\": \"keep\"}.",
			tenancy.AreaInventoryWrite, writeTool, func(ctx context.Context, c *caller, in groupVarsArgs) (any, error) {
				cur, err := getObject(ctx, c, "group", "/inventory/groups/%s", in.GroupID)
				if err != nil {
					return nil, err
				}
				fields, err := schemaOf(cur["schema"])
				if err != nil {
					return nil, err
				}
				vars, err := checkValues([][]schema.Field{fields}, nonNil(in.Vars), keepGroup)
				if err != nil {
					return nil, err
				}
				return send(ctx, c, http.MethodPut, map[string]any{"vars": vars}, ifMatch(in.Revision, cur), "/inventory/groups/%s/vars", in.GroupID)
			}),
		define("add_group_member", "Add group member", "Put a host into a group, with its host-scoped values. Secret fields take only {\"$secret\": \"keep\"}.",
			tenancy.AreaInventoryWrite, writeTool, func(ctx context.Context, c *caller, in memberArgs) (any, error) {
				cur, err := getObject(ctx, c, "group", "/inventory/groups/%s", in.GroupID)
				if err != nil {
					return nil, err
				}
				fields, err := schemaOf(cur["schema"])
				if err != nil {
					return nil, err
				}
				vars, err := checkValues([][]schema.Field{fields}, nonNil(in.Vars), keepGroup)
				if err != nil {
					return nil, err
				}
				res, err := send(ctx, c, http.MethodPut, map[string]any{"vars": vars}, "", "/inventory/groups/%s/members/%s", in.GroupID, in.HostID)
				if err != nil {
					return nil, err
				}
				// A schema change that landed between the check above and
				// the write may have made one of these values' fields
				// secret: such a value is taken out again.
				after, err := getObject(ctx, c, "group", "/inventory/groups/%s", in.GroupID)
				if err != nil {
					return nil, fmt.Errorf("check the schema after the write: %w", err)
				}
				afterFields, err := schemaOf(after["schema"])
				if err != nil {
					return nil, err
				}
				if err := clearNewSecrets(ctx, c, in.GroupID, in.HostID, vars, afterFields, newSecretPaths(fields, afterFields)); err != nil {
					return nil, err
				}
				return res, nil
			}),
		define("remove_group_member", "Remove group member", "Take a host out of a group. This drops its membership values in that group; the host itself stays.",
			tenancy.AreaInventoryWrite, writeTool, func(ctx context.Context, c *caller, in memberRemoveArgs) (any, error) {
				return send(ctx, c, http.MethodDelete, nil, "", "/inventory/groups/%s/members/%s", in.GroupID, in.HostID)
			}),
		define("create_repo", "Create repository", "Add a git repository: URL, branch, subpath, git credential and vault identities by reference.",
			tenancy.AreaReposWrite, writeTool, func(ctx context.Context, c *caller, in repoCreateArgs) (any, error) {
				return send(ctx, c, http.MethodPost, in, "", "/repos")
			}),
		define("update_repo", "Update repository", "Change a repository. Fields: "+strings.Join(repoKeys, ", ")+". Repositories have no revision, so a concurrent change is overwritten.",
			tenancy.AreaReposWrite, writeTool, func(ctx context.Context, c *caller, in repoUpdateArgs) (any, error) {
				return update(ctx, c, http.MethodPut, "repo", repoKeys, updateArgs{Fields: in.Fields}, nil, false, "/repos/%s", in.RepoID)
			}),
		define("sync_repo", "Sync repository", "Fetch the repository again. It touches no host.",
			tenancy.AreaReposWrite, writeTool, func(ctx context.Context, c *caller, in repoInput) (any, error) {
				return send(ctx, c, http.MethodPost, nil, "", "/repos/%s/sync", in.RepoID)
			}),
		define("create_playbook", "Create playbook", "Register a playbook file of a repository, so job templates can use it.",
			tenancy.AreaReposWrite, writeTool, func(ctx context.Context, c *caller, in playbookCreateArgs) (any, error) {
				return send(ctx, c, http.MethodPost, in, "", "/playbooks")
			}),
		define("create_job_template", "Create job template", "Add a job template: playbook, scope, limit, extra vars, SSH credential and vault identities by reference. Secret extra vars take only {\"$secret\": \"keep\"}.",
			tenancy.AreaTemplatesWrite, writeTool, func(ctx context.Context, c *caller, in templateCreateArgs) (any, error) {
				if in.ExtraVars != nil {
					fields, err := schemaOf(in.ExtraVarsSchema)
					if err != nil {
						return nil, err
					}
					vars, err := checkValues([][]schema.Field{fields}, in.ExtraVars, keepTemplate)
					if err != nil {
						return nil, err
					}
					in.ExtraVars = vars
				}
				return send(ctx, c, http.MethodPost, in, "", "/job-templates")
			}),
		define("update_job_template", "Update job template", "Change a job template. Fields: "+strings.Join(templateKeys, ", ")+". Secret extra vars take only {\"$secret\": \"keep\"}; secret schema fields keep their type. The extra_vars_schema field is {\"fields\": [...]}: "+schemaFieldWords+".",
			tenancy.AreaTemplatesWrite, writeTool, func(ctx context.Context, c *caller, in templateUpdateArgs) (any, error) {
				return update(ctx, c, http.MethodPut, "template", templateKeys, in.updateArgs, templateSecrets, true, "/job-templates/%s", in.TemplateID)
			}),
	}
}

// templateSecrets applies the secret rules to a merged template body: the
// stored schema's secret fields keep their type, and extra vars pass the
// value rule under both the stored and the submitted schema.
func templateSecrets(cur, body map[string]any) error {
	old, err := schemaOf(cur["extra_vars_schema"])
	if err != nil {
		return err
	}
	updated, err := schemaOf(body["extra_vars_schema"])
	if err != nil {
		return err
	}
	if err := checkSchemaChange(old, updated); err != nil {
		return err
	}
	if err := checkNewSecretsEmpty(old, updated, asObject(cur["extra_vars"])); err != nil {
		return err
	}
	vars, _ := body["extra_vars"].(map[string]any)
	if vars == nil {
		return nil
	}
	checked, err := checkValues([][]schema.Field{old, updated}, vars, keepTemplate)
	if err != nil {
		return err
	}
	body["extra_vars"] = checked
	return nil
}

// update reads the object, overlays in.Fields on the keys the route takes,
// applies check (if any) and writes, with If-Match when withRevision.
func update(ctx context.Context, c *caller, method, key string, keys []string, in updateArgs, check func(cur, body map[string]any) error, withRevision bool, format string, ids ...string) (any, error) {
	cur, err := getObject(ctx, c, key, format, ids...)
	if err != nil {
		return nil, err
	}
	body, err := merge(cur, keys, in.Fields)
	if err != nil {
		return nil, err
	}
	if check != nil {
		if err := check(cur, body); err != nil {
			return nil, err
		}
	}
	match := ""
	if withRevision {
		match = ifMatch(in.Revision, cur)
	}
	return send(ctx, c, method, body, match, format, ids...)
}

// merge builds an update body from the current object's keys, overlaid
// with fields; a field the route does not take is refused by name.
func merge(cur map[string]any, keys []string, fields map[string]any) (map[string]any, error) {
	var unknown []string
	for k := range fields {
		if !slices.Contains(keys, k) {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown field(s) %s; this tool changes %s", strings.Join(unknown, ", "), strings.Join(keys, ", "))
	}
	body := map[string]any{}
	for _, k := range keys {
		if v, ok := fields[k]; ok {
			if v != nil {
				body[k] = v
			}
			continue
		}
		if v, ok := cur[k]; ok && v != nil && v != "" {
			body[k] = v
		}
	}
	return body, nil
}

// getObject GETs an org route and returns body[key] as a map.
func getObject(ctx context.Context, c *caller, key, format string, ids ...string) (map[string]any, error) {
	path, err := c.orgPath(format, ids...)
	if err != nil {
		return nil, err
	}
	var body map[string]json.RawMessage
	if _, err := c.get(ctx, path, nil, &body); err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(body[key], &obj); err != nil || obj == nil {
		return nil, errors.New("unexpected response from Pulsix")
	}
	return obj, nil
}

// member is one membership as GET .../members returns it.
type member struct {
	HostID string         `json:"host_id"`
	Vars   map[string]any `json:"vars"`
}

// groupMembers reads a group's memberships with their vars.
func groupMembers(ctx context.Context, c *caller, groupID string) ([]member, error) {
	path, err := c.orgPath("/inventory/groups/%s/members", groupID)
	if err != nil {
		return nil, err
	}
	var body struct {
		Members []member `json:"members"`
	}
	if _, err := c.get(ctx, path, nil, &body); err != nil {
		return nil, err
	}
	return body.Members, nil
}

// clearNewSecrets takes a membership's values at paths (fields that just
// became secret) out again, keeping its stored secrets, and reports that
// it did. Nothing at those paths, nothing to do.
func clearNewSecrets(ctx context.Context, c *caller, groupID, hostID string, vars map[string]any, fields []schema.Field, paths []string) error {
	var held []string
	for _, p := range paths {
		for _, v := range valuesAt(vars, p) {
			if v != nil {
				held = append(held, p)
				break
			}
		}
	}
	if len(held) == 0 {
		return nil
	}
	kept, err := checkValues([][]schema.Field{fields}, withoutPaths(vars, held), keepGroup)
	if err != nil {
		return err
	}
	if _, err := send(ctx, c, http.MethodPut, map[string]any{"vars": kept}, "", "/inventory/groups/%s/members/%s", groupID, hostID); err != nil {
		return fmt.Errorf("take out the values of fields that became secret: %w", err)
	}
	return fmt.Errorf("the group's schema changed meanwhile and made %s secret; that value was taken out again, a person sets it in Pulsix", strings.Join(held, ", "))
}

// asObject is v as a JSON object, or nil.
func asObject(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// send writes body to an org route and returns the response body.
func send(ctx context.Context, c *caller, method string, body any, match, format string, ids ...string) (any, error) {
	path, err := c.orgPath(format, ids...)
	if err != nil {
		return nil, err
	}
	res, err := c.do(ctx, method, path, nil, body, match)
	if err != nil {
		return nil, err
	}
	if len(res.Body) == 0 {
		return map[string]any{"ok": true}, nil
	}
	return withoutSchemaLink(res.Body), nil
}

// ifMatch is the If-Match value: the caller's revision, else the one read.
func ifMatch(rev *int64, cur map[string]any) string {
	if rev != nil {
		return `"` + strconv.FormatInt(*rev, 10) + `"`
	}
	if f, ok := cur["revision"].(float64); ok {
		return `"` + strconv.FormatInt(int64(f), 10) + `"`
	}
	return ""
}

// schemaOf decodes a schema as a GET returns it (nil means none).
func schemaOf(v any) ([]schema.Field, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var s schema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	return s.Fields, nil
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
