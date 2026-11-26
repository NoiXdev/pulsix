package triggers

import (
	"strings"

	"github.com/google/uuid"
)

// ChangeSettle is a trigger's settle delay and maximum wait (settle,
// maxWait) after a change that may leave either out: a nil newSettle or
// newMaxWait keeps the current value, a newMaxWait of 0 is the default
// (ten times the delay), and turning the delay off drops a maximum wait
// the change does not name. Every route that rebuilds an Input from a
// partial change goes through it, so an edit that does not mention the
// settle delay never turns it off. The result is checked like any input.
func ChangeSettle(settle int, maxWait *int, newSettle, newMaxWait *int) (int, *int) {
	if newSettle != nil {
		settle = *newSettle
	}
	switch {
	case newMaxWait != nil && *newMaxWait == 0:
		return settle, nil
	case newMaxWait != nil:
		v := *newMaxWait
		return settle, &v
	case settle == 0 || maxWait == nil:
		return settle, nil
	}
	v := *maxWait
	return settle, &v
}

// supersededByRun is the note a superseded window's deliveries get when a
// later delivery started a run in their place.
const supersededByRun = "superseded by run "

// SupersededBy is the run that took the place of a collected delivery
// whose window a later delivery superseded (the trigger's settle delay was
// turned off while it waited), nil when there is none.
func (d Delivery) SupersededBy() *uuid.UUID {
	if d.Outcome != OutcomeCollected {
		return nil
	}
	i := strings.LastIndex(d.Reason, supersededByRun)
	if i < 0 {
		return nil
	}
	rest := d.Reason[i+len(supersededByRun):]
	if j := strings.IndexByte(rest, ';'); j >= 0 {
		rest = rest[:j]
	}
	id, err := uuid.Parse(strings.TrimSpace(rest))
	if err != nil {
		return nil
	}
	return &id
}
