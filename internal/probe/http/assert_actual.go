package http

import (
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/assert"
	shared "github.com/noixdev/pulsix/internal/probe/judge"
)

// The actuals that are no value the target sent.
const (
	// actualAbsent: the header, JSON path or selector matched nothing.
	actualAbsent = shared.ActualAbsent
	// actualHidden stands for a Set-Cookie header's value, which is never
	// stored.
	actualHidden = "(hidden)"
)

// numberActual prints a numeric subject's value without a unit, as every
// plugin's assertions do (judge.NumberActual).
func numberActual(s assert.Subject, v float64) string { return shared.NumberActual(s, v) }

// textActual is a text the target sent, quoted, cleaned and cut as every
// plugin's assertions do (judge.TextActual).
func textActual(s string) string { return shared.TextActual(s) }

// shortActual is an actual cleaned and cut like every label quoting what
// a target sent.
func shortActual(s string) string { return probe.ShortLabel(s) }
