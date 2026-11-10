package varsource

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

func sampleReport(n int) Report {
	r := Report{Commit: "abc", Notes: []Note{}}
	for i := range n {
		f := schema.Field{Name: "v", Type: schema.TypeString, Description: strings.Repeat("d", 500), Origin: schema.OriginSource,
			Source: &schema.FieldSource{Kind: schema.SourceMarker, File: fmt.Sprintf("p%d.yml", i), Line: 3}}
		r.Playbooks = append(r.Playbooks, Playbook{Path: fmt.Sprintf("p%d.yml", i), Fields: []schema.Field{f},
			SetInPlaybook: []string{}, Roles: []RoleUse{}, Files: []FileUse{{Path: fmt.Sprintf("p%d.yml", i), State: FileRead}}, Notes: []Note{}})
	}
	return r
}

func TestEncodeFitsTheCap(t *testing.T) {
	r := sampleReport(10)
	full, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("under the cap", func(t *testing.T) {
		data, out, err := Encode(r, len(full))
		if err != nil || !bytes.Equal(data, full) || !reflect.DeepEqual(out, r) {
			t.Fatalf("err %v, changed = %v", err, !bytes.Equal(data, full))
		}
	})
	t.Run("over the cap", func(t *testing.T) {
		max := len(full) / 2
		data, out, err := Encode(r, max)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > max {
			t.Fatalf("%d bytes, cap %d", len(data), max)
		}
		kept := len(out.Playbooks)
		if kept == 0 || kept >= 10 || !out.Incomplete {
			t.Fatalf("kept %d, incomplete %v", kept, out.Incomplete)
		}
		if !reflect.DeepEqual(out.Playbooks, r.Playbooks[:kept]) {
			t.Fatalf("kept playbooks are not the first %d", kept)
		}
		var want []string
		for _, pb := range r.Playbooks[kept:] {
			want = append(want, pb.Path)
		}
		if !reflect.DeepEqual(out.LeftOut, want) {
			t.Fatalf("left out = %q, want %q", out.LeftOut, want)
		}
		wantNotes(t, out.Notes, "warning report_too_large :0")
		if !strings.Contains(out.Notes[0].Message, want[0]) {
			t.Fatalf("message %q does not name %s", out.Notes[0].Message, want[0])
		}
		var decoded Report
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded.Playbooks) != kept || !decoded.Incomplete || !reflect.DeepEqual(decoded.LeftOut, want) {
			t.Fatalf("decoded: %d playbooks, incomplete %v, left out %q", len(decoded.Playbooks), decoded.Incomplete, decoded.LeftOut)
		}
		one := r
		one.Playbooks = r.Playbooks[:kept+1]
		if more, _ := json.Marshal(one); len(more) <= max {
			t.Fatalf("keeping %d playbooks would fit too", kept+1)
		}
	})
	t.Run("not even the record fits", func(t *testing.T) {
		if _, _, err := Encode(r, 10); err == nil {
			t.Fatal("err = nil")
		}
	})
}

// aliasedReport is the audit's alias fixture without the parser: n playbooks
// whose fields all share one 60 KiB default, so the report is small in memory
// and each is about defaults x 60 KiB of JSON (backlog 7.106).
func aliasedReport(n, defaults int) Report {
	shared := strings.Repeat("a", 60<<10)
	fields := make([]schema.Field, defaults)
	for i := range fields {
		fields[i] = schema.Field{Name: fmt.Sprintf("opt_%d", i), Type: schema.TypeString, Default: shared}
	}
	r := Report{Commit: "abc", Notes: []Note{}}
	for i := range n {
		r.Playbooks = append(r.Playbooks, Playbook{Path: fmt.Sprintf("pb%04d.yml", i), Fields: fields,
			SetInPlaybook: []string{}, Roles: []RoleUse{}, Files: []FileUse{}, Notes: []Note{}})
	}
	return r
}

// TestEncodeBoundsAllocation: encoding a report whose JSON is gigabytes must
// allocate a small multiple of the cap, not the size of the whole report
// (backlog 7.106).
func TestEncodeBoundsAllocation(t *testing.T) {
	r := aliasedReport(1000, 18) // about 1.1 MiB of JSON per playbook, 1.1 GiB in all
	one, err := json.Marshal(r.Playbooks[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(one) < MaxReportBytes/8 {
		t.Fatalf("fixture playbook is %d bytes, want at least %d", len(one), MaxReportBytes/8)
	}

	// The race detector makes sync.Pool drop a quarter of what is put back,
	// and encoding/json then rebuilds a buffer, about 6 MB for a playbook of
	// this size. So the count varies from run to run by whole buffers, and
	// the test takes the best of a few runs: the 11 GiB of the full marshal
	// is out of reach of any run. A collection clears the pool too, so the
	// collector is paused (TotalAlloc is a running total, pausing is free).
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	// Backstop: a regression to the full marshal allocates gigabytes per run,
	// and with the collector paused would keep them; the limit makes the
	// collector run again before the machine swaps.
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(1 << 30))
	var data []byte
	var out Report
	var got uint64
	for range 10 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		var err error
		data, out, err = Encode(r, MaxReportBytes)
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err)
		}
		got = after.TotalAlloc - before.TotalAlloc
		if got > 16*MaxReportBytes {
			// Not pool noise (whole buffers, tens of MB): a real regression.
			t.Fatalf("Encode allocated %d bytes, want at most %d", got, 4*MaxReportBytes)
		}
		if got <= 4*MaxReportBytes {
			break
		}
	}
	if got > 4*MaxReportBytes {
		t.Fatalf("Encode allocated %d bytes in each of 10 runs, want at most %d", got, 4*MaxReportBytes)
	}

	if len(data) > MaxReportBytes {
		t.Fatalf("%d bytes, cap %d", len(data), MaxReportBytes)
	}
	kept := len(out.Playbooks)
	if kept == 0 || kept >= 1000 || !out.Incomplete {
		t.Fatalf("kept %d, incomplete %v", kept, out.Incomplete)
	}
	if len(out.LeftOut) != 1000-kept || out.LeftOut[0] != r.Playbooks[kept].Path {
		t.Fatalf("left out %d names, first %q; kept %d", len(out.LeftOut), out.LeftOut[0], kept)
	}
	wantNotes(t, out.Notes, "warning report_too_large :0")
	var decoded Report
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Playbooks) != kept || !decoded.Incomplete || len(decoded.LeftOut) != 1000-kept {
		t.Fatalf("decoded: %d playbooks, incomplete %v, %d left out", len(decoded.Playbooks), decoded.Incomplete, len(decoded.LeftOut))
	}
	// The longest prefix: one more playbook would not fit.
	if len(data)+len(one) <= MaxReportBytes {
		t.Fatalf("%d bytes kept, one more playbook of %d would fit", len(data), len(one))
	}
}

// TestEncodeEqualsMarshalOfWhatItReturns: the spliced output is exactly what
// json.Marshal gives for the returned report, at every cap (backlog 7.106).
func TestEncodeEqualsMarshalOfWhatItReturns(t *testing.T) {
	r := sampleReport(10)
	r.Commit = `a"playbooks":[]<&>`
	r.Notes = []Note{{Level: LevelHint, Code: "x", Message: `"playbooks":[]`}}
	r.Playbooks[3].Path = `p"3<>.yml`
	full, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	fitted := 0
	for limit := len(full) + 5; limit >= 0; limit -= 41 {
		data, out, err := Encode(r, limit)
		if err != nil {
			continue
		}
		fitted++
		want, merr := json.Marshal(out)
		if merr != nil || !bytes.Equal(data, want) || len(data) > limit {
			t.Fatalf("cap %d: %d bytes, equal to Marshal(out) = %v", limit, len(data), bytes.Equal(data, want))
		}
	}
	if fitted < 5 {
		t.Fatalf("only %d caps encoded", fitted)
	}
	t.Run("no playbooks", func(t *testing.T) {
		empty := Report{Commit: "abc"}
		data, out, err := Encode(empty, 1000)
		want, _ := json.Marshal(empty)
		if err != nil || !bytes.Equal(data, want) || !reflect.DeepEqual(out, empty) {
			t.Fatalf("data %s, err %v", data, err)
		}
	})
	t.Run("the first playbook alone is over the cap", func(t *testing.T) {
		big := sampleReport(1)
		bare, _ := json.Marshal(Report{Commit: "abc", Playbooks: []Playbook{}, Notes: []Note{}})
		data, out, err := Encode(big, len(bare)+400)
		if err != nil || len(out.Playbooks) != 0 || !out.Incomplete || len(out.LeftOut) != 1 {
			t.Fatalf("%s, err %v, %+v", data, err, out)
		}
	})
}
