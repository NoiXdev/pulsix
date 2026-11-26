package http

import (
	"github.com/noixdev/pulsix/internal/probe/assert"
	"golang.org/x/net/html"
)

// pseudoHolds tests a pseudo-class on e; a :not tests its compounds,
// charging them like any other.
func (s *selection) pseudoHolds(p assert.Pseudo, e *element) (ok, enough bool) {
	pos := e.pos
	switch p.Kind {
	case assert.PseudoFirst:
		return pos.index == 1, true
	case assert.PseudoLast:
		return pos.index == pos.count, true
	case assert.PseudoOnly:
		return pos.count == 1, true
	case assert.PseudoNth:
		return nthHolds(p, pos.index), true
	case assert.PseudoNthLast:
		return nthHolds(p, pos.count-pos.index+1), true
	case assert.PseudoFirstOfType:
		return pos.typeIndex == 1, true
	case assert.PseudoLastOfType:
		return pos.typeIndex == pos.typeCount, true
	case assert.PseudoOnlyOfType:
		return pos.typeCount == 1, true
	case assert.PseudoNthOfType:
		return nthHolds(p, pos.typeIndex), true
	case assert.PseudoNthLastOfType:
		return nthHolds(p, pos.typeCount-pos.typeIndex+1), true
	case assert.PseudoEmpty:
		return e.n.FirstChild == nil, true
	case assert.PseudoRoot:
		return e.n.Parent != nil && e.n.Parent.Type == html.DocumentNode, true
	case assert.PseudoNot:
		for _, c := range p.Not {
			ok, enough := s.compound(e, c)
			if !enough || ok {
				return false, enough
			}
		}
		return true, true
	}
	return false, true
}

// nthHolds reports whether the 1-based position i is A·n+B for some
// n >= 0.
func nthHolds(p assert.Pseudo, i int) bool {
	if i < 1 {
		return false
	}
	if p.A == 0 {
		return i == p.B
	}
	d := i - p.B
	return d%p.A == 0 && d/p.A >= 0
}
