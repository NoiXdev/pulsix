//go:build !windows

package main

// runPlatform runs the agent in the foreground; only Windows has a service
// manager the binary must talk to.
func runPlatform(args, env []string) int {
	return run(args, env)
}
