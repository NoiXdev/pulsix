package checkplugins

import (
	"fmt"

	"github.com/noixdev/pulsix/internal/probe/assert"
)

// withAssertions gives a synthetic descriptor other than http the
// assertions parameter (backlog 3.87): each entry "name=expression
// [@warn]", in the plugin's dialect (internal/probe/assert, its format
// here), reports on item "name" whether it held (<plugin>.assertion:
// pass, or its severity) and what was seen (<plugin>.assertion_actual).
// The whole-host item "" is always asked, an assertion's item only while
// an entry names it, and no two entries may name the same item.
func withAssertions(d Descriptor, format Format, example string) Descriptor {
	d.Params = append(d.Params, ParamSpec{
		Name: "assertions", Type: TypeList, MaxEntries: assert.MaxEntries, MaxBytes: assert.MaxEntryLen, Format: format, Example: example,
	})
	d.ItemParam = "assertions"
	d.HostItemAlwaysAsked = true
	d.ItemAsked = func(_ map[string]any, entries []string, item string) bool { return namedEntryAsked(entries, item) }
	d.Metrics = append(d.Metrics,
		// Item: an assertions entry's name. pass, or the entry's severity
		// (warn, crit) when it failed; and what was seen.
		MetricSpec{Name: d.Name + ".assertion", Kind: Discrete},
		MetricSpec{Name: d.Name + ".assertion_actual", Kind: Discrete},
	)
	own := d.Check
	d.Check = func(params map[string]any) error {
		if own != nil {
			if err := own(params); err != nil {
				return err
			}
		}
		assertions, _ := stringList(params["assertions"])
		if name, twice := duplicateEntryName(assertions); twice {
			return fmt.Errorf("assertions names %q twice", name)
		}
		return nil
	}
	return d
}

// dialectProblem is FormatAssertion's rule in dialect d: the entry must
// parse; example shows a valid one.
func dialectProblem(d assert.Dialect, example, s string) string {
	if _, err := d.Parse(s); err != nil {
		return fmt.Sprintf("must be name=assertion such as %s (see the docs): %q: %s", example, s, err)
	}
	return ""
}
