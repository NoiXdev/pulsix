package requirements

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// plainRoleName is a role name galaxy can install under without leaving the
// roles directory: one path component, no leading dot or dash, no
// whitespace. geerlingguy.nginx, my_role, role-name and x@1 are plain.
var plainRoleName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+@-]*$`)

// plan is what one requirements file asks for (spec goal 3).
type plan struct {
	name               string   // the file relative to the requirements root (log lines, errors)
	roles, collections bool     // which install runs
	sources            []string // source strings for checkSources (Task 3), in file order
	values             []value  // other values galaxy may put on a command line, in file order
	gitSources         []string // collection names and sources galaxy may clone with git, in file order
}

// value is a role's or a collection's value other than its source that
// galaxy may hand to git (checkSources): field is its name in messages.
type value struct{ field, text string }

// parseFile checks the shape of one requirements file the way ansible-galaxy
// reads it and says which installs run: galaxy fails on an empty document and
// on a list handed to collection install, so the worker decides itself.
//
// Accepted: an empty document, null or an empty list (nothing to install); a
// list (the legacy roles format); a mapping with only roles and collections,
// each a list or null. Refused before galaxy runs: anything else, include:
// in a role list (it would read any file on the worker), and a role name,
// given or taken from the source, that is not a plain directory name
// (plainRoleName; galaxy extracts the role to <roles>/<name>), and a
// collection name galaxy installs under that is not namespace.name
// (checkCollection), and a Mercurial source (refuseMercurial). The file must
// never read one way here and another way in galaxy, so YAML that the two
// loaders read differently is refused as well: text other than UTF-8 with \n
// or \r\n breaks, invisible characters in the text or in a decoded value, a
// second document, anchors, aliases, merge keys, explicit tags (decodePlain)
// and duplicate keys (galaxy keeps the last).
func parseFile(name string, data []byte) (plan, error) {
	p := plan{name: name}
	doc, err := decodePlain(data)
	switch {
	case errors.Is(err, errOddText), errors.Is(err, errSecondDocument), errors.Is(err, errNotPlainYAML):
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

// addRoleString reads a role string as galaxy's
// RoleRequirement.role_yaml_parse does (ansible-core 2.21.5): with one comma
// it is "src,version", with two "src,version,name", each stripped of Python's
// whitespace first (pyStrip); without a comma it is the source as it stands,
// not stripped; with more galaxy refuses the line before it installs
// anything. Without a third part the name is taken from src, before src's
// "scm+" prefix is cut. A src whose prefix is "hg+" is refused
// (refuseMercurial).
func (p *plan) addRoleString(role string) error {
	var src string
	switch strings.Count(role, ",") {
	case 0:
		src = role
	case 1:
		var version string
		src, version, _ = strings.Cut(pyStrip(role), ",")
		p.values = append(p.values, value{"version", version})
	case 2:
		parts := strings.SplitN(pyStrip(role), ",", 3)
		p.sources = append(p.sources, parts[0])
		p.values = append(p.values, value{"version", parts[1]}, value{"name", parts[2]})
		if hasSCM(parts[0], "hg") {
			return p.refuseMercurial(parts[0])
		}
		return p.checkName(parts[2])
	default:
		return nil
	}
	p.sources = append(p.sources, src)
	if hasSCM(src, "hg") {
		return p.refuseMercurial(src)
	}
	return p.checkTakenName(roleNameFromSource(src))
}

// addRoleMapping reads a role mapping as galaxy's role_yaml_parse does: the
// name is role:, else name:, else taken from src after its "scm+" prefix. The
// scm is that prefix of src; a src without a "+" leaves it to scm:. With a
// role: key galaxy returns early, without the "+" split, so scm: is kept
// whatever src holds. Only src is split: without one there is no prefix, and
// a "+" in the name leaves scm: in force. A Mercurial scm is refused
// (refuseMercurial), quoting the source the plan recorded (src, else the
// name).
func (p *plan) addRoleMapping(e *yaml.Node) error {
	fields := mappingFields(e)
	if _, ok := fields["include"]; ok {
		return fmt.Errorf("%s: include: is not supported; list the roles in this file", p.name)
	}
	p.addValues(e, map[string]string{"scm": "scm", "version": "version", "name": "name", "role": "name"})
	src, hasSrc := fields["src"]
	nameNode, hasName := fields["role"]
	if !hasName {
		nameNode, hasName = fields["name"]
	}
	var from string
	switch {
	case hasSrc:
		from = scalarText(src)
		if isString(src) {
			p.sources = append(p.sources, src.Value)
		}
	case hasName:
		from = scalarText(nameNode)
		if isString(nameNode) {
			p.sources = append(p.sources, nameNode.Value)
		}
	}
	_, hasRole := fields["role"]
	scmNode, scmGiven := fields["scm"]
	if hasSCM(from, "hg") || scmGiven && scalarText(scmNode) == "hg" && (hasRole || !hasSrc || !strings.Contains(from, "+")) {
		return p.refuseMercurial(from)
	}
	switch {
	case hasName:
		return p.checkName(scalarText(nameNode))
	case hasSrc:
		s := scalarText(src)
		if _, after, ok := strings.Cut(s, "+"); ok {
			s = after
		}
		return p.checkTakenName(roleNameFromSource(s))
	}
	return nil
}

// hasSCM reports whether src starts with "scm+": galaxy's role_yaml_parse
// takes the text before src's first "+" as the scm.
func hasSCM(src, scm string) bool {
	before, _, ok := strings.Cut(src, "+")
	return ok && before == scm
}

// refuseMercurial refuses a Mercurial source: galaxy runs hg for it and the
// worker image has none. A local path after hg+ is refused as well
// as a URL.
func (p *plan) refuseMercurial(src string) error {
	return fmt.Errorf("%s: the source %q is a Mercurial source; Mercurial sources are not supported", p.name, src)
}

// addValues records a mapping's scalar values under the keys in fields
// (key → the field's name in messages), in file order: galaxy may hand
// them to git (checkSources). A number is recorded as written: galaxy
// passes its text on.
func (p *plan) addValues(m *yaml.Node, fields map[string]string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		field, ok := fields[m.Content[i].Value]
		if text := scalarText(m.Content[i+1]); ok && text != "" {
			p.values = append(p.values, value{field, text})
		}
	}
}

// checkName refuses a role name given in the file (name:, role: or a role
// string's third part) that is not a plain directory name (Ruling 8): galaxy
// installs the role to <roles>/<name>.
func (p *plan) checkName(name string) error {
	if !plainRoleName.MatchString(name) {
		return notRequirements(p.name, "the role name %q is not a plain directory name", name)
	}
	return nil
}

// checkTakenName refuses a role name galaxy takes from the role's source when
// it is not a plain directory name: a local source such as ../../r.tar.gz is
// its own name, and galaxy extracts to <roles>/<name>. Such a role needs a
// name:.
func (p *plan) checkTakenName(name string) error {
	if !plainRoleName.MatchString(name) {
		return notRequirements(p.name, "the role name %q, taken from its source, is not a plain directory name; give the role a name:", name)
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

// mappingFields indexes a mapping's values by their scalar keys. The file has
// no duplicate keys, merge keys or aliases (parseFile), so this is the
// mapping galaxy reads.
func mappingFields(m *yaml.Node) map[string]*yaml.Node {
	fields := make(map[string]*yaml.Node, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i]; k.Kind == yaml.ScalarNode {
			fields[k.Value] = m.Content[i+1]
		}
	}
	return fields
}

// scalarText is the text a value is checked as when it is a role name: a
// scalar's text whatever type yaml.v3 gives it (galaxy's loader may read
// .5e3 as a string where yaml.v3 reads a number), and the empty name for
// null or a list or mapping.
func scalarText(n *yaml.Node) string {
	if n.Kind != yaml.ScalarNode || n.ShortTag() == "!!null" {
		return ""
	}
	return n.Value
}

// pyStrip is Python's str.strip() without arguments: it trims what
// str.isspace() reports at both ends (pySpace).
func pyStrip(s string) string {
	return strings.TrimFunc(s, pySpace)
}

// pySpace is Python's str.isspace() for one character: Unicode White_Space
// as Go's unicode.IsSpace has it, plus the information separators
// U+001C-U+001F. Written out so it never follows Go's set by accident.
func pySpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', '\x1c', '\x1d', '\x1e', '\x1f', ' ',
		'\u0085', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

func notRequirements(name, format string, args ...any) error {
	return fmt.Errorf("%s is not a requirements file: %s", name, fmt.Sprintf(format, args...))
}
