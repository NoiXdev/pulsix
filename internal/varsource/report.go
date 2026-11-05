package varsource

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Report is what a repo sync read: the commit, every discovered playbook's
// sources, and the notes about the repo itself. Incomplete: a cap left
// playbooks out (LeftOut names them); they are neither read nor gone.
// Failed: the repository as a whole could not be read (a note says why);
// the report's empty playbook list says nothing about the repository, and
// the previous record stands.
type Report struct {
	Commit     string     `json:"commit"`
	Failed     bool       `json:"failed,omitempty"`
	Playbooks  []Playbook `json:"playbooks"`
	Incomplete bool       `json:"incomplete"`
	LeftOut    []string   `json:"left_out,omitempty"`
	Notes      []Note     `json:"notes"`
}

// Encode renders r as JSON of at most maxBytes. A report too large keeps
// the most playbooks from the front that fit: the rest are dropped, named
// in LeftOut and in one report_too_large warning, and the report is
// Incomplete. It returns the report it encoded. An error means not even
// the report without playbooks fits.
func Encode(r Report, maxBytes int) ([]byte, Report, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, r, fmt.Errorf("encode variable sources: %w", err)
	}
	if len(data) <= maxBytes {
		return data, r, nil
	}
	// Keeping fewer playbooks never makes the report larger (a dropped
	// playbook's path is shorter than the playbook), so search the most
	// that fit.
	lo, hi := 0, len(r.Playbooks)-1
	var best []byte
	var bestReport Report
	for lo <= hi {
		keep := (lo + hi) / 2
		cut := dropPlaybooks(r, keep, maxBytes)
		data, err := json.Marshal(cut)
		if err != nil {
			return nil, r, fmt.Errorf("encode variable sources: %w", err)
		}
		if len(data) <= maxBytes {
			best, bestReport, lo = data, cut, keep+1
		} else {
			hi = keep - 1
		}
	}
	if best == nil {
		return nil, r, fmt.Errorf("encode variable sources: the report does not fit %d bytes even without playbooks", maxBytes)
	}
	return best, bestReport, nil
}

// dropPlaybooks is r with only its first keep playbooks.
func dropPlaybooks(r Report, keep, maxBytes int) Report {
	dropped := make([]string, 0, len(r.Playbooks)-keep)
	for _, pb := range r.Playbooks[keep:] {
		dropped = append(dropped, pb.Path)
	}
	out := r
	out.Playbooks = r.Playbooks[:keep:keep]
	out.Incomplete = true
	out.LeftOut = append(slices.Clip(r.LeftOut), dropped...)
	out.Notes = append(slices.Clip(r.Notes), warning(codeReportTooLarge, "", 0,
		"the variable sources are larger than %d bytes; %d playbooks are left out: %s", maxBytes, len(dropped), nameList(dropped, len(dropped))))
	return out
}
