package varsource

import "gopkg.in/yaml.v3"

// dupsNoted tells which mappings of a file had their duplicate keys noted,
// for a file parsed more than once: a role spec file is parsed again for
// each entry point it is used for (backlog 7.113). A mapping an earlier
// parse noted is not noted again, as it was not when one parse served
// every entry point.
//
// A mapping is known by its number in document order (depth first, each
// key before its value, aliases not followed). That number is the same in
// every parse of the same bytes, and no two mappings of a parse share one,
// not even a mapping used as the first key of another, which starts at
// the same line and column. What stays between parses is one bit per
// mapping up to the last one noted.
type dupsNoted struct {
	doc   *yaml.Node         // the document of the parse being read
	order map[*yaml.Node]int // its mappings numbered, built on the first need
	bits  []uint64
}

// parsed starts a parse of the file whose document is doc.
func (d *dupsNoted) parsed(doc *yaml.Node) { d.doc, d.order = doc, nil }

// release lets the parse's nodes go; the bits stay.
func (d *dupsNoted) release() {
	if d != nil {
		d.doc, d.order = nil, nil
	}
}

// lookup is mapping n's number, -1 when d is nil or n is not one of the
// parse's mappings, and whether an earlier parse noted its keys.
func (d *dupsNoted) lookup(n *yaml.Node) (int, bool) {
	if d == nil || d.doc == nil {
		return -1, false
	}
	if d.order == nil {
		d.number()
	}
	i, ok := d.order[n]
	if !ok {
		return -1, false
	}
	return i, i/64 < len(d.bits) && d.bits[i/64]&(1<<(i%64)) != 0
}

// mark records that mapping i's keys were noted.
func (d *dupsNoted) mark(i int) {
	for len(d.bits) <= i/64 {
		d.bits = append(d.bits, 0)
	}
	d.bits[i/64] |= 1 << (i % 64)
}

// number numbers the parse's mappings in document order. It visits each
// node of the tree once, as the parse did.
func (d *dupsNoted) number() {
	d.order = map[*yaml.Node]int{}
	stack := []*yaml.Node{d.doc}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.Kind == yaml.MappingNode {
			d.order[n] = len(d.order)
		}
		for i := len(n.Content) - 1; i >= 0; i-- {
			stack = append(stack, n.Content[i])
		}
	}
}
