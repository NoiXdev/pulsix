package varsource

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
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
