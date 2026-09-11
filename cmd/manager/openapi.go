package main

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/noixdev/pulsix/internal/api"
)

// runOpenAPI writes the manager's OpenAPI document to w and returns the
// process exit code. It needs no configuration, no database and no
// listening server (see api.Document's own doc comment for why) — this is
// what makes `pulsix-manager openapi` safe to run in CI, piped straight
// into openapi-typescript, without standing up a Postgres container first.
func runOpenAPI(w io.Writer) int {
	doc, err := api.Document()
	if err != nil {
		slog.Error("failed to build openapi document", "err", err)
		return 1
	}
	if _, err := fmt.Fprintln(w, string(doc)); err != nil {
		slog.Error("failed to write openapi document", "err", err)
		return 1
	}
	return 0
}
