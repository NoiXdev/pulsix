package assistantscript_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/noixdev/pulsix/internal/assistantscript"
)

// scripts/assistant-mcp.sh is canonical (CI tests it directly); go:embed
// cannot reach it from here, so a copy lives next to this package. This test
// keeps the two equal.
func TestEmbeddedCopyMatchesCanonicalScript(t *testing.T) {
	canonical, err := os.ReadFile("../../scripts/assistant-mcp.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(assistantscript.Script(), canonical) {
		t.Fatal("internal/assistantscript/assistant-mcp.sh differs from scripts/assistant-mcp.sh; refresh it with: cp scripts/assistant-mcp.sh internal/assistantscript/assistant-mcp.sh")
	}
}
