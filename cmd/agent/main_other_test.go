//go:build !windows

package main

import "testing"

// protectEnvFile is a no-op here: a 0600 file owned by the test user is
// already acceptable on Unix.
func protectEnvFile(*testing.T, string) {}
