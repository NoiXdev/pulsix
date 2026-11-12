//go:build !windows

package disk

// driveKindOf is nil off Windows: only the Windows rule asks for a drive kind.
var driveKindOf func(mount string) string
