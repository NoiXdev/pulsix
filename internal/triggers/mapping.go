package triggers

import (
	"errors"
	"fmt"
	"strings"

	"github.com/theory/jsonpath"
	"github.com/theory/jsonpath/spec"

	"github.com/noixdev/pulsix/internal/templates"
)

const (
	maxPathLen      = 256
	maxMappedVars   = 50
	errLimitMessage = "limit must map to strings"
)

// compiledMapping is a validated mapping ready to apply to payloads.
type compiledMapping struct {
	vars  map[string]*jsonpath.Path
	limit *jsonpath.Path
}

func compileMapping(m Mapping) (compiledMapping, error) {
	c := compiledMapping{vars: make(map[string]*jsonpath.Path, len(m.ExtraVars))}
	if len(m.ExtraVars) > maxMappedVars {
		return c, fmt.Errorf("%w: at most %d mapped variables", ErrInvalidInput, maxMappedVars)
	}
	for name, expr := range m.ExtraVars {
		p, err := parsePath(expr)
		if err != nil {
			return c, fmt.Errorf("%w: mapping for %s: %v", ErrInvalidInput, name, err)
		}
		c.vars[name] = p
	}
	if m.Limit != "" {
		p, err := parsePath(m.Limit)
		if err != nil {
			return c, fmt.Errorf("%w: mapping for limit: %v", ErrInvalidInput, err)
		}
		c.limit = p
	}
	return c, nil
}

// parsePath accepts the RFC 9535 subset of spec §3: root, then child
// segments with exactly one name, index or wildcard selector each.
func parsePath(expr string) (*jsonpath.Path, error) {
	if expr == "" || len(expr) > maxPathLen {
		return nil, fmt.Errorf("a path is 1-%d characters", maxPathLen)
	}
	p, err := jsonpath.Parse(expr)
	if err != nil {
		// The parser's own message, not a guess at the mistake: this
		// used to say "start with $" for EVERY parse failure, so an
		// operator whose path did start with $ and broke somewhere
		// else was sent looking at the one character that was right
		// (backlog 2.68). The expression is the operator's own
		// configuration, never payload, so quoting the parser is safe.
		return nil, fmt.Errorf("not a usable JSONPath expression: %v", err)
	}
	for _, seg := range p.Query().Segments() {
		if seg.IsDescendant() {
			return nil, errors.New("descendant segments (..) are not supported")
		}
		sels := seg.Selectors()
		if len(sels) != 1 {
			return nil, errors.New("use one name, index or [*] per segment")
		}
		switch sels[0].(type) {
		case spec.Name, spec.Index, spec.WildcardSelector:
		default:
			return nil, errors.New("only names, indexes and [*] are supported (no filters, slices or functions)")
		}
	}
	return p, nil
}

func hasWildcard(p *jsonpath.Path) bool {
	for _, seg := range p.Query().Segments() {
		if _, ok := seg.Selectors()[0].(spec.WildcardSelector); ok {
			return true
		}
	}
	return false
}

// apply maps a decoded payload. A missing path leaves its target unset; a
// wildcard path yields a list (joined with "," for limit). On error the
// second result names the offending field — never its value.
func (c compiledMapping) apply(doc any) (templates.Overrides, string, error) {
	var out templates.Overrides
	for name, p := range c.vars {
		nodes := selectOrdered(p, doc)
		if len(nodes) == 0 {
			continue
		}
		if out.ExtraVars == nil {
			out.ExtraVars = map[string]any{}
		}
		if hasWildcard(p) {
			out.ExtraVars[name] = nodes
		} else {
			out.ExtraVars[name] = nodes[0]
		}
	}
	if c.limit != nil {
		nodes := selectOrdered(c.limit, doc)
		if len(nodes) > 0 {
			parts := make([]string, 0, len(nodes))
			for _, n := range nodes {
				s, ok := n.(string)
				if !ok {
					return templates.Overrides{}, "limit", errors.New(errLimitMessage)
				}
				parts = append(parts, s)
			}
			limit := strings.Join(parts, ",")
			out.Limit = &limit
		}
	}
	return out, "", nil
}

// selectOrdered is Path.Select with a defined order (backlog 2.62).
//
// A [*] selector over a JSON OBJECT walks the decoded map, and Go
// randomises map iteration — so the same payload produced a differently
// ordered list on every delivery. For a mapped extra var that is a list
// whose order a playbook may act on; for the limit it is worse, because
// the parts are joined with "," and the run then records a different
// host pattern each time, with no way to reproduce what actually ran.
//
// SelectLocated pairs each node with its normalized path, and the
// library's own Sort orders by it: object keys lexically, array indices
// NUMERICALLY (spec.NormalizedPath.Compare). So an array's own order —
// which was never random — survives, including past index 9.
func selectOrdered(p *jsonpath.Path, doc any) []any {
	located := p.SelectLocated(doc)
	located.Sort()
	out := make([]any, 0, len(located))
	for _, n := range located {
		out = append(out, n.Node)
	}
	return out
}
