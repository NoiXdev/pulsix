package requirements

import (
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// namespaceName is a collection name galaxy may install under: galaxy
// installs to <path>/<namespace>/<name> and removes that directory first
// (ansible-core 2.21.5, ansible/galaxy/collection/__init__.py:1500,1507-1508).
var namespaceName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)

// mightBeCollectionName holds every string galaxy's is_valid_collection_name
// accepts (utils/collection_loader/_collection_finder.py:1058-1074: one dot,
// both parts Python identifiers). An identifier holds no ASCII character
// other than letters, digits and _, so a string with another one, or with
// no dot or two, is never a collection name to galaxy. Non-ASCII characters
// are let through: identifiers may hold them.
var mightBeCollectionName = regexp.MustCompile(`^(?:\w|[^\x00-\x7f])*\.(?:\w|[^\x00-\x7f])*$`)

// addCollections reads a collection list: each entry a name or a mapping
// whose name and source are sources, and whose name galaxy may install
// under is namespace.name (checkCollection).
func (p *plan) addCollections(list *yaml.Node) error {
	for i, e := range list.Content {
		var err error
		switch {
		case isString(e):
			// galaxy reads a string as {name: <string>} (cli/galaxy.py:838-841).
			p.sources = append(p.sources, e.Value)
			err = p.checkUntypedName(e.Value)
		case e.Kind == yaml.MappingNode:
			fields := mappingFields(e)
			p.addValues(e, map[string]string{"name": "name", "version": "version", "type": "type"})
			for _, field := range []string{"name", "source"} {
				if v, ok := fields[field]; ok && isString(v) {
					p.sources = append(p.sources, v.Value)
				}
			}
			err = p.checkCollection(fields)
		default:
			err = notRequirements(p.name, "collections entry %d is neither a name nor a mapping", i+1)
		}
		if err != nil {
			return err
		}
		p.collections = true
	}
	return nil
}

// checkCollection follows galaxy's Requirement.from_requirement_dict
// (galaxy/dependency_resolution/dataclasses.py:330-483) to the name it
// installs under. name, type and source are read with dict.get, so a null
// one is a missing one (:338-342).
//
//   - No type: the name is kept when is_valid_collection_name accepts it
//     (:362-365); otherwise it is the path or URL of the artifact (:366-370),
//     or galaxy fails (:388). checkUntypedName.
//   - type galaxy: the name is kept as written; nothing checks it.
//   - Another type with a source: the name is kept as written (:449, :473).
//   - Another type without a source: the name becomes the source and is
//     cleared (:449-450).
//
// Every name kept as written must be namespace.name. A cleared or missing
// name is read from the artifact itself (get_direct_collection_fqcn, :473-474:
// a tarball's MANIFEST.json, a directory's galaxy.yml or MANIFEST.json), as
// are the names of its dependencies. That data is the collection's, not this
// file's: the install running as the play uid confines what it can reach
// (later tasks). git and subdirs requirements are virtual and are not
// installed under their own name (dataclasses.py:560-561,
// collection/__init__.py:738), but the rule is the same for every type.
func (p *plan) checkCollection(fields map[string]*yaml.Node) error {
	name, hasName := nonNull(fields, "name")
	typ, hasType := nonNull(fields, "type")
	_, hasSource := nonNull(fields, "source")
	switch {
	case !hasName:
		return nil
	case !hasType:
		return p.checkUntypedName(scalarText(name))
	case scalarText(typ) == "galaxy" || hasSource:
		return p.checkCollectionName(scalarText(name))
	}
	return nil
}

// checkUntypedName checks a name without a type: galaxy keeps it when it
// might be a collection name (mightBeCollectionName) and treats it as a path
// or URL otherwise.
func (p *plan) checkUntypedName(name string) error {
	if !mightBeCollectionName.MatchString(name) {
		return nil
	}
	return p.checkCollectionName(name)
}

func (p *plan) checkCollectionName(name string) error {
	if !namespaceName.MatchString(name) {
		return fmt.Errorf("%s: the collection name %q is not namespace.name", p.name, name)
	}
	return nil
}

// nonNull is a mapping field that is present and not null.
func nonNull(fields map[string]*yaml.Node, key string) (*yaml.Node, bool) {
	n, ok := fields[key]
	if !ok || n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" {
		return nil, false
	}
	return n, true
}
