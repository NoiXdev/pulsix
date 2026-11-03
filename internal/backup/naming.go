package backup

import (
	"strings"
	"time"
)

const (
	objectPrefix = "pulsix-"
	// objectSuffix ends the name of every backup object; listings ignore
	// anything else, so partial uploads and stray files stay invisible.
	objectSuffix = ".pxbak"
	// timestampLayout is the UTC stamp inside an object name.
	timestampLayout = "20060102T150405Z"
)

// ObjectName is the file or object name of a backup created at createdAt, in
// UTC: pulsix-20261102T030000Z-scheduled.pxbak.
func ObjectName(createdAt time.Time, kind string) string {
	return objectPrefix + createdAt.UTC().Format(timestampLayout) + "-" + kind + objectSuffix
}

// ParseObjectName is the inverse of ObjectName. ok is false for any name
// ObjectName could not have produced, aliases of a valid name included.
func ParseObjectName(name string) (createdAt time.Time, kind string, ok bool) {
	rest, found := strings.CutPrefix(name, objectPrefix)
	if !found {
		return time.Time{}, "", false
	}
	rest, found = strings.CutSuffix(rest, objectSuffix)
	if !found {
		return time.Time{}, "", false
	}
	stamp, kind, found := strings.Cut(rest, "-")
	if !found || (kind != "scheduled" && kind != "manual") {
		return time.Time{}, "", false
	}
	at, err := time.ParseInLocation(timestampLayout, stamp, time.UTC)
	if err != nil {
		return time.Time{}, "", false
	}
	// The time parser also takes a fraction after the seconds ("030000.0Z",
	// "030000,0Z"); only the one spelling ObjectName writes is a backup's
	// name, so no backup can be listed under an alias.
	if ObjectName(at, kind) != name {
		return time.Time{}, "", false
	}
	return at, kind, true
}
