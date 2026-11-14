package script

import "context"

// errUnavailable is the checker's refusal while the agent has no real
// runner and checker (backlog 3.6g, Task 4 replaces both).
var errUnavailable = refuse("running commands is not available on this system")

// unavailable is a runner and checker that refuse everything: every
// program is refused before it could run.
type unavailable struct{}

func (unavailable) Check(string) error { return errUnavailable }

func (unavailable) Run(context.Context, []string) (runResult, error) {
	return runResult{}, errUnavailable
}
