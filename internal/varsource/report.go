package varsource

import (
	"bytes"
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
// the longest prefix of playbooks that fits: the rest are dropped, named in
// LeftOut and in one report_too_large warning, and the report is
// Incomplete. It returns the report it encoded; the bytes are what
// json.Marshal gives for that report. An error means not even the report
// without playbooks fits.
//
// The limit is a parameter so that every report with a byte cap shares
// this encoder (the repo sync passes MaxReportBytes).
//
// Encode never builds more than the kept JSON plus one playbook: it
// encodes the playbooks in order into one buffer that cannot grow past
// maxBytes, stops at the first that does not fit, and splices the
// envelope (commit, notes, left-out names) around the kept ones. A
// marshal of the whole report would need several times the size of a
// report that is gigabytes (backlog 7.106).
func Encode(r Report, maxBytes int) ([]byte, Report, error) {
	if len(r.Playbooks) == 0 {
		data, err := json.Marshal(r)
		if err != nil {
			return nil, r, fmt.Errorf("encode variable sources: %w", err)
		}
		if len(data) > maxBytes {
			return nil, r, fmt.Errorf("encode variable sources: the report does not fit %d bytes even without playbooks", maxBytes)
		}
		return data, r, nil
	}

	bare, err := marshalBare(r)
	if err != nil {
		return nil, r, err
	}
	if len(bare.head)+1+len(bare.tail) > maxBytes {
		return nil, r, fmt.Errorf("encode variable sources: the report does not fit %d bytes even without playbooks", maxBytes)
	}

	// ends[k] is where playbook k ends in the buffer; the buffer starts
	// with the head, and playbooks follow one another after commas.
	buf := &cappedBuffer{max: maxBytes}
	buf.write(bare.head)
	ends := make([]int, 0, len(r.Playbooks))
	enc := json.NewEncoder(buf)
	for i, pb := range r.Playbooks {
		if i > 0 {
			buf.write([]byte{','})
			if buf.overflowed {
				break
			}
		}
		if err := enc.Encode(pb); err != nil {
			return nil, r, fmt.Errorf("encode variable sources: %w", err)
		}
		if buf.overflowed {
			break
		}
		ends = append(ends, buf.len())
	}

	if len(ends) == len(r.Playbooks) && buf.len()+1+len(bare.tail) <= maxBytes {
		return buf.finish(ends[len(ends)-1], bare.tail), r, nil
	}

	// Keeping fewer playbooks never makes the report larger (a dropped
	// playbook's path is shorter than the playbook), so search the most
	// that fit. Only the envelope is re-marshalled per probe.
	lo, hi := 0, min(len(ends), len(r.Playbooks)-1)
	best := -1
	var bestTail []byte
	var bestReport Report
	for lo <= hi {
		keep := (lo + hi) / 2
		cut := dropPlaybooks(r, keep, maxBytes)
		cutBare, err := marshalBare(cut)
		if err != nil {
			return nil, r, err
		}
		if kept(ends, keep, len(bare.head))+1+len(cutBare.tail) <= maxBytes {
			best, bestTail, bestReport, lo = keep, cutBare.tail, cut, keep+1
		} else {
			hi = keep - 1
		}
	}
	if best < 0 {
		return nil, r, fmt.Errorf("encode variable sources: the report does not fit %d bytes even without playbooks", maxBytes)
	}
	return buf.finish(kept(ends, best, len(bare.head)), bestTail), bestReport, nil
}

// kept is the buffer length after the first keep playbooks.
func kept(ends []int, keep, headLen int) int {
	if keep == 0 {
		return headLen
	}
	return ends[keep-1]
}

// bareJSON is a report's JSON cut at its playbook list: head ends with the
// list's opening bracket, tail starts with its closing one.
type bareJSON struct{ head, tail []byte }

const playbooksKey = `"playbooks":[`

// marshalBare marshals r without its playbooks and cuts the result at the
// empty playbook list. A quote inside a string value is escaped, so the key
// cannot be found in the commit or in a note by mistake.
func marshalBare(r Report) (bareJSON, error) {
	r.Playbooks = []Playbook{}
	data, err := json.Marshal(r)
	if err != nil {
		return bareJSON{}, fmt.Errorf("encode variable sources: %w", err)
	}
	i := bytes.Index(data, []byte(playbooksKey))
	if i < 0 {
		return bareJSON{}, fmt.Errorf("encode variable sources: no playbook list in %.40q", data)
	}
	i += len(playbooksKey)
	return bareJSON{head: data[:i], tail: data[i+1:]}, nil
}

// cappedBuffer collects JSON up to max bytes. The write that would pass max
// is dropped whole and every later write with it (overflowed): what is in
// the buffer is always a whole number of writes. The capacity never passes
// max, and a report that outgrows an eighth of it gets max at once, so a
// report of any size costs about max.
type cappedBuffer struct {
	b          []byte
	max        int
	overflowed bool
}

func (c *cappedBuffer) len() int { return len(c.b) }

func (c *cappedBuffer) write(p []byte) {
	if c.overflowed {
		return
	}
	if len(c.b)+len(p) > c.max {
		c.overflowed = true
		return
	}
	if need := len(c.b) + len(p); need > cap(c.b) {
		// Doubling up to an eighth of max, then max itself: a big report
		// would otherwise allocate every size in between.
		next := max(2*cap(c.b), need, 4096)
		if next > c.max/8 {
			next = c.max
		}
		grown := make([]byte, len(c.b), next)
		copy(grown, c.b)
		c.b = grown
	}
	c.b = append(c.b, p...)
}

// Write implements io.Writer for json.Encoder, which ends a value with a
// newline that is not part of the playbook.
func (c *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	c.write(bytes.TrimSuffix(p, []byte{'\n'}))
	return n, nil
}

// finish is the first n bytes, the playbook list's closing bracket and tail.
// The caller sized n and tail to fit max, so this allocates only when the
// buffer's capacity is short.
func (c *cappedBuffer) finish(n int, tail []byte) []byte {
	out := c.b[:n]
	if n+1+len(tail) > cap(out) {
		out = append(make([]byte, 0, n+1+len(tail)), out...)
	}
	out = append(out, ']')
	return append(out, tail...)
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
