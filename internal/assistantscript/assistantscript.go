// Package assistantscript embeds the assistant connect script that the
// manager serves at GET /assistant-mcp.sh.
//
// scripts/assistant-mcp.sh is the canonical copy (CI tests it directly).
// The embed directive cannot reach outside the package directory, so the
// assistant-mcp.sh next to this file is a copy, and the package test fails
// when they differ.
package assistantscript

import _ "embed"

//go:embed assistant-mcp.sh
var script []byte

// Script returns the script's bytes. The slice is shared: callers must not
// modify it.
func Script() []byte { return script }
