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
		return nil, errors.New("not a JSONPath expression (start with $)")
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
		nodes := p.Select(doc)
		if len(nodes) == 0 {
			continue
		}
		if out.ExtraVars == nil {
			out.ExtraVars = map[string]any{}
		}
		if hasWildcard(p) {
			out.ExtraVars[name] = []any(nodes)
		} else {
			out.ExtraVars[name] = nodes[0]
		}
	}
	if c.limit != nil {
		nodes := c.limit.Select(doc)
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
