package requirements

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// namespaceName is a collection name galaxy may install under: galaxy
// installs to <path>/<namespace>/<name> and removes that directory first
// (ansible-core 2.21.5, ansible.galaxy.collection.install).
var namespaceName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)

// mightBeCollectionName holds every string galaxy's is_valid_collection_name
// accepts (AnsibleCollectionRef.is_valid_collection_name: one dot, both parts
// Python identifiers). An identifier holds no ASCII character
// other than letters, digits and _, so a string with another one, or with
// no dot or two, is never a collection name to galaxy. Non-ASCII characters
// are let through: identifiers may hold them.
var mightBeCollectionName = regexp.MustCompile(`^(?:\w|[^\x00-\x7f])*\.(?:\w|[^\x00-\x7f])*$`)

// addCollections reads a collection list: each entry a name or a mapping
// whose name and source are sources, and whose name galaxy may install
// under is namespace.name (checkCollection). A string, name or source that
// starts with "hg+" is a Mercurial source and is refused (refuseMercurial).
func (p *plan) addCollections(list *yaml.Node) error {
	for i, e := range list.Content {
		var err error
		switch {
		case isString(e):
			// galaxy reads a string as {name: <string>} (GalaxyCLI._init_coll_req_dict).
			p.sources = append(p.sources, e.Value)
			p.gitSources = append(p.gitSources, e.Value)
			if strings.HasPrefix(e.Value, "hg+") {
				return p.refuseMercurial(e.Value)
			}
			err = p.checkUntypedName(e.Value)
		case e.Kind == yaml.MappingNode:
			fields := mappingFields(e)
			p.addValues(e, map[string]string{"name": "name", "version": "version", "type": "type", "source": "source"})
			for _, field := range []string{"name", "source"} {
				if v, ok := fields[field]; ok && isString(v) {
					if strings.HasPrefix(v.Value, "hg+") {
						return p.refuseMercurial(v.Value)
					}
					p.sources = append(p.sources, v.Value)
					p.gitSources = append(p.gitSources, v.Value)
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

// checkCollection follows galaxy's
// _ComputedReqKindsMixin.from_requirement_dict to the name it installs under
// (ansible-core 2.21.5). name, type and source are read with dict.get, so a
// null one is a missing one.
//
//   - No type: the name is kept when is_valid_collection_name accepts it (the
//     valid-name branch); otherwise it is the path or URL of the artifact (the
//     pointer branch), or galaxy fails. checkUntypedName.
//   - type galaxy: the name is kept as written; nothing checks it.
//   - Another type with a source: the name is kept as written.
//   - Another type without a source: the name becomes the source and is
//     cleared (the source swap).
//
// Every name kept as written must be namespace.name. A cleared or missing
// name is read from the artifact itself (the get_direct_collection_fqcn call
// in from_requirement_dict, ConcreteArtifactsManager.get_direct_collection_fqcn:
// a tarball's MANIFEST.json, a directory's galaxy.yml or MANIFEST.json), as
// are the names of its dependencies. That data is the collection's, not this
// file's: the install running as the play uid confines what it can reach
// (later tasks). git and subdirs requirements are virtual
// (_ComputedReqKindsMixin.is_virtual) and install_collections does not
// install them under their own name, but the rule is the same for every type:
// a git or subdirs name is refused too although galaxy installs no collection
// under it. That is conservative and kept.
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
//
// A Python keyword is the one name galaxy's is_valid_collection_name refuses
// that mightBeCollectionName lets through (ns.class): from_requirement_dict
// then does not keep it as a name but reads it as a local path, through
// _is_concrete_artifact_pointer (a file, a collection directory or a
// namespace directory in galaxy's working directory), or fails. That is no
// parser differential that escapes the check: such a string is both a source
// (resolved and, as a directory, walked by checkSources) and a kept name,
// which checkCollectionName lets through only as two plain components. Both
// readings are checked, so ns.class stays accepted.
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
