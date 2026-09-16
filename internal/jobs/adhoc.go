// The ad-hoc command enqueue (spec §7, backlog 2.14): an operator runs one
// shell command on explicit hosts — one job per zone the hosts live in,
// all sharing a run group, exactly the split EnqueueHostJob does. v0.2b
// ships module shell only; the module field exists so v0.3 can extend the
// vocabulary without a schema change.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
)

// AdhocModule is v0.2b's only ad-hoc module. The enqueue validates it and
// the runner's ad-hoc parser refuses anything else, so the two ends fail
// closed on the same name.
const AdhocModule = "shell"

// maxAdhocArgv is the ad-hoc command's size bound. The API bounds the body
// field with huma's maxLength at the same value and the runner defends the
// bound again at claim time (internal/worker/runner/playbook.go).
const maxAdhocArgv = 8 << 10

// adhocKindPayload is the kind payload an ad-hoc job stores: which module
// to run and the command itself. The argv is operator-provided, not secret
// material, but the payload rides EnqueueHostJob's sealing path
// (unconditional for host jobs) and is opened again at claim time by the
// queue's openKindPayload — the same sealed round trip bootstrap and
// rotate_key use, so no separate unsealed path is needed.
type adhocKindPayload struct {
	Module string `json:"module"`
	Argv   string `json:"argv"`
}

// EnqueueAdhoc enqueues one ad-hoc shell command on explicit hosts. It
// validates the module and the argv, then splits the targets per zone —
// one job per zone, all sharing a run group — and returns every created
// job, so the API can answer 202 with the whole group. Validation is all
// or nothing: a bad module, a bad argv or one bad host fails the whole
// enqueue before any job row exists.
func (s *Service) EnqueueAdhoc(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID, module, argv string, actor *uuid.UUID) ([]Job, error) {
	if module != AdhocModule {
		return nil, fmt.Errorf("%w: module %q is not supported (shell is v0.2b's only ad-hoc module)", ErrInvalidInput, module)
	}
	if strings.TrimSpace(argv) == "" {
		return nil, fmt.Errorf("%w: argv is empty", ErrInvalidInput)
	}
	if len(argv) > maxAdhocArgv {
		return nil, fmt.Errorf("%w: argv exceeds %d bytes", ErrInvalidInput, maxAdhocArgv)
	}
	if !utf8.ValidString(argv) {
		return nil, fmt.Errorf("%w: argv is not valid UTF-8", ErrInvalidInput)
	}
	if len(hostIDs) == 0 {
		return nil, fmt.Errorf("%w: at least one host is required", ErrInvalidInput)
	}
	// Every target must exist and be managed, and a host of another
	// organization must be as invisible as one that does not exist. An
	// unmanaged host maps onto the same sentinel: it cannot run anything,
	// so from the caller's view it is simply not there (404 at the API).
	// EnqueueHostJob keeps its own InvalidInput semantics for its other
	// kinds; the ad-hoc route deliberately answers 404.
	byZone := map[uuid.UUID][]uuid.UUID{}
	var zoneOrder []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, id := range hostIDs {
		if seen[id] {
			continue // a repeated id targets the host once
		}
		seen[id] = true
		h, err := s.hosts.GetHost(ctx, orgID, id)
		if err != nil {
			if errors.Is(err, hosts.ErrNotFound) {
				err = ErrNotFound
			}
			return nil, fmt.Errorf("host %s: %w", id, err)
		}
		if !h.Managed {
			return nil, fmt.Errorf("%w: host %s is not managed", ErrNotFound, id)
		}
		if _, ok := byZone[h.ZoneID]; !ok {
			zoneOrder = append(zoneOrder, h.ZoneID)
		}
		byZone[h.ZoneID] = append(byZone[h.ZoneID], id)
	}
	payload, err := json.Marshal(adhocKindPayload{Module: module, Argv: argv})
	if err != nil {
		return nil, fmt.Errorf("encode ad-hoc payload: %w", err)
	}
	return s.insertHostJobs(ctx, orgID, KindAdhoc, byZone, zoneOrder, payload, TriggerManual, actor)
}
