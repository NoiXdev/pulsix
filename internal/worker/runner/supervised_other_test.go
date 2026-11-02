//go:build !linux

package runner

// cleanupSupervisorBuild has nothing to remove: the play supervisor is
// built only for the Linux tests.
func cleanupSupervisorBuild() {}
