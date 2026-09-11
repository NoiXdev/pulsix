// This file exists only to fence web/ off from the root Go module's build
// graph (module boundaries stop `go list`/`go test`/`go build`'s `./...`
// pattern from descending further). Without it, `go test ./...` (and
// scripts/coverage-gate.sh, which relies on it) run from the repo root
// walk into web/node_modules once `npm install` has populated it — some
// npm packages (e.g. flatted, used transitively by a dev dependency) ship
// a stray .go file for unrelated reasons, and the root module would
// otherwise treat it as one of its own packages, failing the coverage
// gate on a 0%-covered package nobody wrote or owns.
//
// web/ is a Vite/TypeScript project and has no actual Go code of its own;
// this module is never built or imported by anything.
module pulsix-web-boundary

go 1.27
