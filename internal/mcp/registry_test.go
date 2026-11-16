package mcp

import (
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestEveryToolRegisters: building every tool's schema must not panic (a
// panic would turn every initialize into a 500).
func TestEveryToolRegisters(t *testing.T) {
	for _, tl := range registry() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: %v", tl.name, r)
				}
			}()
			tl.register(sdk.NewServer(&sdk.Implementation{Name: "t"}, nil), &caller{})
		}()
	}
}
