// The assistant endpoint end to end (spec docs/superpowers/specs/
// 2026-11-17-assistant-token-edit-design.md, "Part 3"): real assistant
// tokens drive /api/mcp of a fully wired manager — Postgres, the worker,
// the SSH host behind its bastion, the git server and the fake
// ansible-runner — through every tool and area, a run request with its
// approval and follow-up, every refusal, the secret sweep and a token edit.
// The phases share one scenario and run in order.
package e2e

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// toolOutput is one tool call's result text, kept for the secret sweep.
type toolOutput struct {
	tool string
	text string
}

// assistantSuite is the state the phases of TestAssistantEndToEnd share.
type assistantSuite struct {
	s *scenario
	// root is the test itself: what must outlive one phase (the database
	// handle) is opened on it, not on the phase's subtest.
	root *testing.T
	ctx  context.Context

	// called names every tool record invoked, and outputs holds each
	// result: record is what the every_tool and run_request phases call
	// tools with.
	called  map[string]bool
	outputs []toolOutput

	seeded assistantSeed
	// allToken is the plaintext of the token holding every area, allTokenID
	// its id.
	allToken   string
	allTokenID string
	// allTools is what tools/list returns for allToken, in its order.
	allTools []string
}

// chainTick is the follow-up starter's tick: an approved run's follow-up
// starts within it once the run succeeded.
const chainTick = 200 * time.Millisecond

func TestAssistantEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("no docker for the testcontainer database: %v", err)
	}
	if os.Getenv("PULSIX_E2E_REAL_ANSIBLE") == "1" {
		t.Skip("the assistant scenario drives the fake runner")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := wireScenario(t, ctx, cancel, wiring{opts: []app.Option{app.WithChainTick(chainTick)}})
	a := &assistantSuite{s: s, root: t, ctx: ctx, called: map[string]bool{}}

	// Each phase builds on the one before, so a failed phase stops the run.
	phases := []struct {
		name string
		run  func()
	}{
		{"seed", a.seed},
		{"tools_list", a.toolsList},
		{"every_tool", a.everyTool},
		{"run_request", a.runRequest},
		{"refusals", a.refusals},
		{"secrets", a.secrets},
		{"edit", a.edit},
	}
	for _, p := range phases {
		ok := t.Run(p.name, func(t *testing.T) {
			// The scenario's helpers fail through s.t and s.api.t: point
			// both at the subtest.
			prev := s.t
			s.t, s.api.t = t, t
			defer func() { s.t, s.api.t = prev, prev }()
			p.run()
		})
		if !ok {
			t.Fatalf("phase %s failed; the later phases depend on it", p.name)
		}
	}
}

// toolsList is phase 2: a token per area lists exactly that area's tools,
// and a token holding every area lists the union of the table.
func (a *assistantSuite) toolsList() {
	s := a.s
	s.t.Helper()
	union := map[string]bool{}
	for _, area := range tenancy.Areas() {
		want, ok := expectedTools[area]
		if !ok {
			s.t.Errorf("area %s has no row in expectedTools", area)
			continue
		}
		for _, n := range want {
			union[n] = true
		}
		tok := s.createAssistantToken("asst-"+string(area), area)
		got := a.listTools(a.mustConnect(tok.Token))
		if !slices.Equal(sortedCopy(got), sortedCopy(want)) {
			s.t.Errorf("tools/list for %s\nunexpected: %v\nmissing: %v", area, minus(got, want), minus(want, got))
		}
	}
	if len(expectedTools) != len(tenancy.Areas()) {
		s.t.Errorf("expectedTools has %d rows for %d areas", len(expectedTools), len(tenancy.Areas()))
	}
	all := s.createAssistantToken("asst-all", tenancy.Areas()...)
	a.allToken, a.allTokenID = all.Token, all.ID
	a.allTools = a.listTools(a.mustConnect(all.Token))
	if want := keys(union); !slices.Equal(sortedCopy(a.allTools), sortedCopy(want)) {
		s.t.Fatalf("tools/list for every area is not the table's union\nwithout a row: %v\nnot listed: %v", minus(a.allTools, want), minus(want, a.allTools))
	}
}
