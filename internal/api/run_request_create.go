package api

import (
	"time"

	"github.com/noixdev/pulsix/internal/runrequests"
)

// requestRunRequest is the body of a run request (spec 2026-11-25
// assistant API gaps, A): the reason, and what the run changes about the
// template's run. Every override narrows or adjusts the template's run;
// none widens it. The type is named so that its OpenAPI schema keeps the
// name huma gave the inline body before (RequestRunRequest).
type requestRunRequest struct {
	HostID    string         `json:"host_id,omitempty" doc:"Run on this one host only; it must be among the template's targets. Same as host_ids with one host; give one of the two"`
	Reason    string         `json:"reason" maxLength:"300" doc:"Why the run is needed, shown to the person who approves it"`
	CheckMode bool           `json:"check_mode,omitempty" doc:"A dry run: the playbook runs in check mode and changes nothing (tasks marked to run in check mode still run). It needs an approval like any run, and starts no follow-ups"`
	Diff      *bool          `json:"diff,omitempty" doc:"Show the changes a dry run would make; only with check_mode, where it is on unless set to false"`
	ExtraVars map[string]any `json:"extra_vars,omitempty" doc:"Extra vars replacing the template's top-level ones of the same name, checked against the template's schema; secret fields cannot be set here"`
	HostIDs   []string       `json:"host_ids,omitempty" maxItems:"500" doc:"Run on these hosts only; each must be among the template's targets"`
	Limit     string         `json:"limit,omitempty" maxLength:"1000" doc:"A host pattern narrowing the template's own limit: with a template limit, one pattern (a host, group or glob, or one !exclusion)"`
	ExpiresIn int            `json:"expires_in,omitempty" doc:"Minutes the request stays approvable, 5 to 1440; 60 when omitted"`
}

// input is the body as runrequests.CreateInput, without the template and
// the requester.
func (b requestRunRequest) input() (runrequests.CreateInput, error) {
	in := runrequests.CreateInput{
		Kind: runrequests.KindRun, Reason: b.Reason, ExpiresIn: time.Duration(b.ExpiresIn) * time.Minute,
		Overrides: runrequests.OverridesInput{CheckMode: b.CheckMode, Diff: b.Diff, ExtraVars: b.ExtraVars},
	}
	if b.HostID != "" {
		id, err := parseUUID(b.HostID, "host_id")
		if err != nil {
			return runrequests.CreateInput{}, err
		}
		in.HostID = &id
	}
	for _, raw := range b.HostIDs {
		id, err := parseUUID(raw, "host_ids")
		if err != nil {
			return runrequests.CreateInput{}, err
		}
		in.Overrides.HostIDs = append(in.Overrides.HostIDs, id)
	}
	if b.Limit != "" {
		in.Overrides.Limit = &b.Limit
	}
	return in, nil
}
