package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestNewLoggerTeesIntoTheRing: every line goes to the log's destination
// and into the status page's ring (backlog 3.132).
func TestNewLoggerTeesIntoTheRing(t *testing.T) {
	var out bytes.Buffer
	logger, ring := newLogger(&out)
	logger.Info("hello", "k", "v")
	if !strings.Contains(out.String(), "msg=hello") {
		t.Fatalf("destination = %q", out.String())
	}
	lines := ring.Lines()
	if len(lines) != 1 || !strings.Contains(lines[0], "msg=hello k=v") {
		t.Fatalf("ring = %q", lines)
	}
}
