// Playbook YAML validation: the one gate an editor playbook passes before
// it is stored.
package playbooks

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// MaxInlineBytes is the stored playbook size limit; a bigger editor text is
// rejected before it ever reaches the database.
const MaxInlineBytes = 512 * 1024

// ValidateYAML checks that inline is the beginning of a usable Ansible
// playbook: at most 512 KiB of YAML, whose document node is a non-empty
// sequence whose first element is a mapping carrying hosts as a non-empty
// string. Only the first YAML document is considered — the same rule
// Ansible's own loader applies — and only the first play is inspected: the
// worker's pre-run syntax check is the real gate for the plays after it, so
// the editor never rejects a playbook the full parser would have accepted.
// Later plays unchecked, documented here on purpose.
//
// The error messages are part of the surface (they are shown to the
// operator as-is): "expected a list of plays", "play 1: must be a mapping",
// "play 1: missing hosts", "play 1: hosts must be a string",
// "malformed YAML: <cause>" and "playbook exceeds 512 KiB". Every failure
// wraps ErrInvalidInput.
func ValidateYAML(inline string) error {
	if len(inline) > MaxInlineBytes {
		return fmt.Errorf("%w: playbook exceeds 512 KiB", ErrInvalidInput)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(inline), &doc); err != nil {
		return fmt.Errorf("%w: malformed YAML: %s", ErrInvalidInput, err)
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("%w: expected a list of plays", ErrInvalidInput)
	}
	root := doc.Content[0]
	if root.Kind != yaml.SequenceNode || len(root.Content) == 0 {
		return fmt.Errorf("%w: expected a list of plays", ErrInvalidInput)
	}
	play := root.Content[0]
	if play.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: play 1: must be a mapping", ErrInvalidInput)
	}
	value, ok := mappingValue(play, "hosts")
	if !ok || isEmptyScalar(value) {
		return fmt.Errorf("%w: play 1: missing hosts", ErrInvalidInput)
	}
	if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return fmt.Errorf("%w: play 1: hosts must be a string", ErrInvalidInput)
	}
	return nil
}

// mappingValue finds key's value in a parsed mapping node.
func mappingValue(play *yaml.Node, key string) (*yaml.Node, bool) {
	for i := 0; i+1 < len(play.Content); i += 2 {
		if k, v := play.Content[i], play.Content[i+1]; k.Kind == yaml.ScalarNode && k.Value == key {
			return v, true
		}
	}
	return nil, false
}

// isEmptyScalar reports a scalar that carries no value: the empty string
// and YAML null (`hosts:` with nothing after it) both read as "forgot to
// fill hosts in".
func isEmptyScalar(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || n.Value == "")
}

// firstPlayName returns the first play's name key when the document parses
// and names one, for list summaries.
func firstPlayName(inline string) (string, bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(inline), &doc); err != nil || len(doc.Content) == 0 {
		return "", false
	}
	root := doc.Content[0]
	if root.Kind != yaml.SequenceNode || len(root.Content) == 0 {
		return "", false
	}
	play := root.Content[0]
	if play.Kind != yaml.MappingNode {
		return "", false
	}
	name, ok := mappingValue(play, "name")
	if !ok || name.Tag != "!!str" || name.Value == "" {
		return "", false
	}
	return name.Value, true
}
