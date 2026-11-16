package assert

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/andybalholm/cascadia"
)

// Matching a CSS selector on one node is cascadia's Match, which cannot be
// interrupted: the probe can stop only between nodes. So the shape of a
// selector is read from what cascadia parsed (its own structure, not the
// text, so comments, escapes and nth expressions cannot hide a combinator
// from the count) and bounded at validation, and each node's worst case is
// charged before Match runs (MatchCost).

// Bounds of a comma-separated part of an html(…) selector.
const (
	// maxDescendants: n descendant combinators make one match walk up to
	// C(depth, n) ancestor combinations; the HTML parser nests at most 512
	// elements, so 3 is about 2*10^7 steps at worst.
	maxDescendants = 3
	// maxSiblingWalks bounds how many sibling or child walks nest: sibling
	// counts are not bounded by the parser.
	maxSiblingWalks = 2
)

var (
	errSelectComment = errors.New("a CSS selector of html(…) may not contain comments (/* … */)")
	errSelectTilde   = errors.New("a CSS selector of html(…) may have at most one ~ combinator in each comma-separated part, and not together with a descendant (space) combinator")
	errSelectEmpty   = errors.New("a CSS selector of html(…) may use :empty only on the element it selects, not on an ancestor or sibling")
	errSelectWalks   = errors.New("a CSS selector of html(…) nests too many searches of ancestors or siblings (descendant and ~ combinators, :lang, :enabled, :disabled, :empty, and the nth-, first-, last- and only- pseudo-classes)")
)

// partCost is how the worst case of matching one comma-separated part on a
// node grows: with C(depth, ancestors) for its nested ancestor walks, with
// siblings^siblings for its nested sibling or child walks (the largest
// count on the node's path), and with the attributes on the path when
// attrs. sibInAnc: a sibling walk runs on each ancestor an ancestor walk
// visits (each costs that ancestor's siblings, SiblingSum in all);
// ancInSib: an ancestor walk runs on each sibling a sibling walk visits
// (the product). Without either the walks run one after the other.
// sibUp: a sibling walk runs on the parent or a further ancestor (through
// >), so it is bounded by the largest count on the path rather than the
// node's own.
// subject is what the element the part selects must be, as far as a
// cheap look can tell.
type partCost struct {
	ancestors, siblings int
	attrs               bool
	sibInAnc, ancInSib  bool
	sibUp               bool
	subject             subjectFilter
}

// subjectOf reads the filter of a part's rightmost compound.
func subjectOf(v reflect.Value) subjectFilter {
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return subjectFilter{}
		}
		v = v.Elem()
	}
	switch v.Type().Name() {
	case "combinedSelector":
		if second := v.FieldByName("second"); !second.IsNil() {
			return subjectOf(second)
		}
		return subjectOf(v.FieldByName("first"))
	case "compoundSelector":
		var f subjectFilter
		list := v.FieldByName("selectors")
		for i := range list.Len() {
			f.add(list.Index(i).Elem())
		}
		return f
	}
	// A compound of one simple selector is that selector.
	var f subjectFilter
	f.add(v)
	return f
}

// add narrows f by one simple selector of the subject.
func (f *subjectFilter) add(m reflect.Value) {
	switch m.Type().Name() {
	case "tagSelector":
		f.tag = m.FieldByName("tag").String()
	case "idSelector":
		f.id = m.FieldByName("id").String()
	case "classSelector":
		f.class = m.FieldByName("class").String()
	}
}

// shape is what the analysis of one selector collects.
type shape struct {
	partCost
	descendants, tildes int
}

func (s shape) max(o shape) shape {
	return shape{
		partCost: partCost{
			ancestors: max(s.ancestors, o.ancestors),
			siblings:  max(s.siblings, o.siblings),
			attrs:     s.attrs || o.attrs,
			sibInAnc:  s.sibInAnc || o.sibInAnc,
			ancInSib:  s.ancInSib || o.ancInSib,
			sibUp:     s.sibUp || o.sibUp,
		},
		descendants: s.descendants + o.descendants,
		tildes:      s.tildes + o.tildes,
	}
}

// selectorCosts checks a selector cascadia parsed from raw and returns the
// cost of each comma-separated part. It refuses comments, the
// pseudo-classes that search a subtree or its text, more than
// maxDescendants descendant combinators or more than one ~ in a part, a ~
// together with a descendant combinator, :empty anywhere but on the
// selected element, and anything this analysis does not know.
func selectorCosts(raw string, group cascadia.SelectorGroup) ([]partCost, error) {
	if strings.Contains(raw, "/*") {
		return nil, errSelectComment
	}
	costs := make([]partCost, len(group))
	for i, sel := range group {
		s, err := analyse(reflect.ValueOf(sel), false)
		if err != nil {
			return nil, err
		}
		switch {
		case s.tildes > 1 || s.tildes == 1 && s.descendants > 0:
			return nil, errSelectTilde
		case s.descendants > maxDescendants:
			return nil, fmt.Errorf("a CSS selector of html(…) may have at most %d descendant (space) combinators in each comma-separated part", maxDescendants)
		case s.ancestors > maxDescendants || s.siblings > maxSiblingWalks:
			return nil, errSelectWalks
		}
		costs[i] = s.partCost
		costs[i].subject = subjectOf(reflect.ValueOf(sel))
	}
	return costs, nil
}

// analyse reads one of cascadia's selector values. elsewhere: the
// selector is matched on nodes other than the one visited (an ancestor or
// a sibling), as the left side of a combinator is.
func analyse(v reflect.Value, elsewhere bool) (shape, error) {
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return shape{}, nil
		}
		v = v.Elem()
	}
	switch v.Type().Name() {
	case "tagSelector", "inputPseudoClassSelector", "rootPseudoClassSelector", "neverMatchSelector":
		return shape{}, nil
	case "classSelector", "idSelector", "attrSelector", "linkPseudoClassSelector", "checkedPseudoClassSelector":
		return shape{partCost: partCost{attrs: true}}, nil
	case "nthPseudoClassSelector", "onlyChildPseudoClassSelector":
		return shape{partCost: partCost{siblings: 1}}, nil
	case "emptyElementPseudoClassSelector":
		if elsewhere {
			return shape{}, errSelectEmpty
		}
		return shape{partCost: partCost{siblings: 1}}, nil
	case "langPseudoClassSelector":
		return shape{partCost: partCost{ancestors: 1, attrs: true}}, nil
	case "enabledPseudoClassSelector", "disabledPseudoClassSelector":
		// The fieldset search walks the ancestors, and a legend among them
		// its siblings.
		return shape{partCost: partCost{ancestors: 1, siblings: 1, attrs: true, sibInAnc: true}}, nil
	case "compoundSelector":
		var s shape
		list := v.FieldByName("selectors")
		for i := range list.Len() {
			m, err := analyse(list.Index(i), elsewhere)
			if err != nil {
				return shape{}, err
			}
			s = s.max(m)
		}
		return s, nil
	case "relativePseudoClassSelector":
		if name := v.FieldByName("name").String(); name != "not" {
			return shape{}, fmt.Errorf("a CSS selector of html(…) may not use :%s, which searches whole subtrees", name)
		}
		var s shape
		group := v.FieldByName("match")
		for i := range group.Len() {
			m, err := analyse(group.Index(i), elsewhere)
			if err != nil {
				return shape{}, err
			}
			s = s.max(m)
		}
		return s, nil
	case "containsPseudoClassSelector":
		return shape{}, errors.New("a CSS selector of html(…) may not use :contains, which searches whole subtrees")
	case "regexpPseudoClassSelector":
		return shape{}, errors.New("a CSS selector of html(…) may not use :matches, which searches whole subtrees")
	case "combinedSelector":
		return analyseCombined(v, elsewhere)
	}
	return shape{}, fmt.Errorf("a CSS selector of html(…) uses %s, which Pulsix cannot bound", v.Type().Name())
}

// analyseCombined: "first <combinator> second". second is matched on the
// node itself, first on its ancestors (space: each of them, >: the parent)
// or its earlier siblings (~: each of them, +: the nearest element, past
// any text and comments).
func analyseCombined(v reflect.Value, elsewhere bool) (shape, error) {
	first, err := analyse(v.FieldByName("first"), true)
	if err != nil {
		return shape{}, err
	}
	second, err := analyse(v.FieldByName("second"), elsewhere)
	if err != nil {
		return shape{}, err
	}
	switch byte(v.FieldByName("combinator").Uint()) {
	case ' ':
		first.sibInAnc = first.sibInAnc || first.siblings > 0
		first.ancestors++
		first.descendants++
	case '~':
		first.ancInSib = first.ancInSib || first.ancestors > 0
		first.siblings++
		first.tildes++
	case '>':
		first.sibUp = first.sibUp || first.siblings > 0
	case '+':
		first.siblings = max(first.siblings, 1)
	}
	return first.max(second), nil
}
