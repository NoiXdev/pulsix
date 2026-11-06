package requirements

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// plan is what one requirements file asks for (spec goal 3).
type plan struct {
	name               string   // the file relative to the requirements root (log lines, errors)
	roles, collections bool     // which install runs
	sources            []string // source strings for checkSources (Task 3), in file order
}

// parseFile checks the shape of one requirements file the way ansible-galaxy
// reads it and says which installs run: galaxy fails on an empty document and
// on a list handed to collection install, so the worker decides itself.
//
// Accepted: an empty document, null or an empty list (nothing to install); a
// list (the legacy roles format); a mapping with only roles and collections,
// each a list or null. Refused before galaxy runs: anything else, include:
// in a role list (it would read any file on the worker), and a role name that
// would put the role outside the roles directory (galaxy extracts the role to
// <roles>/<name>). The file must never read one way here and another way in
// galaxy, so YAML that the two loaders read differently is refused as well:
// a second document, anchors, aliases, merge keys, explicit tags (decodePlain)
// and duplicate keys (galaxy keeps the last).
func parseFile(name string, data []byte) (plan, error) {
	p := plan{name: name}
	doc, err := decodePlain(data)
	switch {
	case errors.Is(err, errSecondDocument), errors.Is(err, errNotPlainYAML):
		return plan{}, notRequirements(name, "%v", err)
	case err != nil:
		return plan{}, notRequirements(name, "invalid YAML: %v", err)
	case doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0:
		return p, nil
	}
	var whole any
	if err := doc.Decode(&whole); err != nil {
		return plan{}, notRequirements(name, "invalid YAML: %v", err)
	}
	switch top := doc.Content[0]; {
	case top.Kind == yaml.ScalarNode && top.ShortTag() == "!!null":
	case top.Kind == yaml.SequenceNode:
		err = p.addRoles(top)
	case top.Kind == yaml.MappingNode:
		err = p.addMapping(top)
	default:
		err = notRequirements(name, "the top level must be a list of roles or a mapping of roles and collections")
	}
	if err != nil {
		return plan{}, err
	}
	return p, nil
}

// addMapping reads the mapping form: only roles and collections, each a list
// or null, in file order.
func (p *plan) addMapping(top *yaml.Node) error {
	for i := 0; i < len(top.Content); i += 2 {
		if k := top.Content[i]; k.Kind != yaml.ScalarNode || k.Value != "roles" && k.Value != "collections" {
			return notRequirements(p.name, "only roles and collections are allowed at the top level, found %s", k.Value)
		}
	}
	for i := 0; i < len(top.Content); i += 2 {
		k, v := top.Content[i].Value, top.Content[i+1]
		switch {
		case v.Kind == yaml.ScalarNode && v.ShortTag() == "!!null":
			continue
		case v.Kind != yaml.SequenceNode:
			return notRequirements(p.name, "%s must be a list", k)
		}
		var err error
		if k == "roles" {
			err = p.addRoles(v)
		} else {
			err = p.addCollections(v)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// addRoles reads a role list: each entry a role string
// ("src[,version[,name]]") or a mapping.
func (p *plan) addRoles(list *yaml.Node) error {
	for i, e := range list.Content {
		var err error
		switch {
		case isString(e):
			err = p.addRoleString(e.Value)
		case e.Kind == yaml.MappingNode:
			err = p.addRoleMapping(e)
		default:
			err = notRequirements(p.name, "roles entry %d is neither a name nor a mapping", i+1)
		}
		if err != nil {
			return err
		}
		p.roles = true
	}
	return nil
}

// addRoleString reads "src[,version[,name]]" as galaxy's role_yaml_parse
// does: the name is the third part, or else taken from src.
func (p *plan) addRoleString(s string) error {
	parts := strings.Split(strings.TrimSpace(s), ",")
	src := parts[0]
	p.sources = append(p.sources, src)
	switch len(parts) {
	case 1, 2:
		return p.checkTakenName(roleNameFromSource(src))
	case 3:
		return p.checkName(parts[2])
	}
	return nil // galaxy refuses the line before it installs anything
}

// addRoleMapping reads a role mapping as galaxy's role_yaml_parse does: the
// name is role:, else name:, else taken from src after its "scm+" prefix.
func (p *plan) addRoleMapping(e *yaml.Node) error {
	var m map[string]any
	if err := e.Decode(&m); err != nil {
		return notRequirements(p.name, "invalid YAML: %v", err)
	}
	if _, ok := m["include"]; ok {
		return fmt.Errorf("%s: include: is not supported; list the roles in this file", p.name)
	}
	src, hasSrc := m["src"].(string)
	nameValue, hasName := m["role"]
	if !hasName {
		nameValue, hasName = m["name"]
	}
	switch {
	case hasSrc:
		p.sources = append(p.sources, src)
	case hasName:
		if s, ok := nameValue.(string); ok {
			p.sources = append(p.sources, s)
		}
	}
	switch {
	case hasName:
		return p.checkName(scalarString(nameValue))
	case hasSrc:
		if _, after, ok := strings.Cut(src, "+"); ok {
			src = after
		}
		return p.checkTakenName(roleNameFromSource(src))
	}
	return nil
}

// addCollections reads a collection list: each entry a name or a mapping
// whose name and source are sources.
func (p *plan) addCollections(list *yaml.Node) error {
	for i, e := range list.Content {
		switch {
		case isString(e):
			p.sources = append(p.sources, e.Value)
		case e.Kind == yaml.MappingNode:
			var m map[string]any
			if err := e.Decode(&m); err != nil {
				return notRequirements(p.name, "invalid YAML: %v", err)
			}
			for _, field := range []string{"name", "source"} {
				if s, ok := m[field].(string); ok {
					p.sources = append(p.sources, s)
				}
			}
		default:
			return notRequirements(p.name, "collections entry %d is neither a name nor a mapping", i+1)
		}
		p.collections = true
	}
	return nil
}

// checkName refuses a role name given in the file that is not one plain
// directory name (Ruling 8): galaxy installs the role to <roles>/<name>.
func (p *plan) checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return notRequirements(p.name, "the role name %q is not a plain directory name", name)
	}
	return nil
}

// checkTakenName refuses a role name galaxy takes from the role's source when
// it leads outside the roles directory: a local source such as
// ../../r.tar.gz is its own name, and galaxy extracts to <roles>/<name>.
// Below the roles directory is fine (./r.tar.gz installs as roles/r.tar.gz).
func (p *plan) checkTakenName(name string) error {
	if clean := path.Clean(name); path.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") {
		return notRequirements(p.name, "the role name %q, taken from its source, leads outside the roles directory", name)
	}
	return nil
}

// roleNameFromSource is galaxy's RoleRequirement.repo_url_to_role_name: a
// source without "://" and "@" is its own name, else the last path part
// without .git, .tar.gz and anything after a comma.
func roleNameFromSource(src string) string {
	if !strings.Contains(src, "://") && !strings.Contains(src, "@") {
		return src
	}
	name := src[strings.LastIndex(src, "/")+1:]
	name = strings.TrimSuffix(name, ".git")
	name = strings.TrimSuffix(name, ".tar.gz")
	name, _, _ = strings.Cut(name, ",")
	return name
}

// isString reports a scalar that YAML reads as a string (not a number, a
// boolean or null).
func isString(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str"
}

// scalarString is how a decoded YAML value reads as a role name: null is the
// empty name.
func scalarString(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func notRequirements(name, format string, args ...any) error {
	return fmt.Errorf("%s is not a requirements file: %s", name, fmt.Sprintf(format, args...))
}
