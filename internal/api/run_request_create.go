package api

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/runrequests"
)

// exactObject is a JSON object kept as its bytes until decode, which reads
// numbers as json.Number: an integer past 2^53 keeps every digit, where
// decoding into map[string]any would round it to a float64.
type exactObject json.RawMessage

// UnmarshalJSON keeps the bytes.
func (o *exactObject) UnmarshalJSON(b []byte) error {
	*o = append((*o)[:0], b...)
	return nil
}

// Schema implements huma.SchemaProvider: any JSON object.
func (exactObject) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, AdditionalProperties: true}
}

// decode reads the object with exact numbers; nil for none (or null).
func (o exactObject) decode() (map[string]any, error) {
	raw := bytes.TrimSpace(o)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, huma.Error422UnprocessableEntity("extra_vars must be a JSON object")
	}
	return out, nil
}

// requestRunRequest is the body of a run request (spec 2026-11-25
// assistant API gaps, A): the reason, and what the run changes about the
// template's run. Every override narrows or adjusts the template's run;
// none widens it. The type is named so that its OpenAPI schema keeps the
// name huma gave the inline body before (RequestRunRequest).
type requestRunRequest struct {
	HostID    string      `json:"host_id,omitempty" doc:"Run on this one host only; it must be among the template's targets. Same as host_ids with one host; give one of the two"`
	Reason    string      `json:"reason" maxLength:"300" doc:"Why the run is needed, shown to the person who approves it"`
	CheckMode bool        `json:"check_mode,omitempty" doc:"A dry run: the playbook runs in check mode and changes nothing (tasks marked to run in check mode still run). It needs an approval like any run, and starts no follow-ups"`
	Diff      *bool       `json:"diff,omitempty" doc:"Show the changes a dry run would make; only with check_mode, where it is on unless set to false"`
	ExtraVars exactObject `json:"extra_vars,omitempty" doc:"Extra vars replacing the template's top-level ones of the same name, checked against the template's schema. Secret fields and names Ansible or Pulsix act on (ansible_…, pulsix_…, Ansible's magic variables and playbook keywords) cannot be set here; the run receives every text in them as literal text, never as a template"`
	HostIDs   []string    `json:"host_ids,omitempty" maxItems:"500" doc:"Run on these hosts only; each must be among the template's targets"`
	Limit     string      `json:"limit,omitempty" maxLength:"1000" doc:"Host names and * wildcards, joined by , or :, with ! before a name to exclude it (no groups, no ? or other patterns): the run keeps to the template's targets whose host name matches, under the template's own limit"`
	ExpiresIn int         `json:"expires_in,omitempty" minimum:"5" maximum:"1440" doc:"Minutes the request stays approvable, 5 to 1440; 60 when omitted"`
}

// input is the body as runrequests.CreateInput, without the template and
// the requester.
func (b requestRunRequest) input() (runrequests.CreateInput, error) {
	vars, err := b.ExtraVars.decode()
	if err != nil {
		return runrequests.CreateInput{}, err
	}
	in := runrequests.CreateInput{
		Kind: runrequests.KindRun, Reason: b.Reason, ExpiresInMinutes: b.ExpiresIn,
		Overrides: runrequests.OverridesInput{CheckMode: b.CheckMode, Diff: b.Diff, ExtraVars: vars},
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

// requestSummary is the audit summary of a run request: a dry run says so,
// and the overrides it asked for are named by field (extra vars by their
// keys), never with a value.
func requestSummary(r runrequests.Request) string {
	what := "a run"
	if r.Overrides != nil && r.Overrides.Check {
		what = "a dry run"
	}
	summary := "requested " + what + " of job template " + r.TemplateID.String()
	if fields := overrideFields(r.Overrides); fields != "" {
		summary += " (overrides: " + fields + ")"
	}
	return summary + ": " + r.Reason
}

// overrideFields names the overrides ov sets, extra vars with their keys
// sorted; empty for none. check_mode is not among them: the summaries say
// "dry run".
func overrideFields(ov *runrequests.Overrides) string {
	if ov == nil {
		return ""
	}
	var parts []string
	if ov.Check && !ov.Diff {
		parts = append(parts, "no diff")
	}
	if len(ov.ExtraVars) > 0 {
		parts = append(parts, "extra_vars ("+strings.Join(slices.Sorted(maps.Keys(ov.ExtraVars)), ", ")+")")
	}
	if len(ov.HostIDs) > 0 {
		parts = append(parts, "host_ids")
	}
	if ov.Limit != nil {
		parts = append(parts, "limit")
	}
	return strings.Join(parts, ", ")
}
