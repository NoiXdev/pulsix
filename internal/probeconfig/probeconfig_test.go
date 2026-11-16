package probeconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

var (
	targetOne = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	targetTwo = uuid.MustParse("00000000-0000-0000-0000-000000000002")
)

func goldenSpecs() []Spec {
	return []Spec{
		{TargetHostID: targetTwo, CheckKey: "web", Plugin: "http", ParamsJSON: `{"url":"https://x"}`, IntervalSeconds: 60, Address: "10.0.0.2"},
		{TargetHostID: targetOne, CheckKey: "ping", Plugin: "icmp", ParamsJSON: "{}", IntervalSeconds: 30, Address: "10.0.0.1"},
	}
}

// TestVersionCanonicalEncoding pins the encoding byte for byte and the
// version it hashes to. The manager and every agent build compare versions
// computed by this function; the encoding must never change.
func TestVersionCanonicalEncoding(t *testing.T) {
	t.Parallel()
	want := "00000000-0000-0000-0000-000000000001\x00ping\x00icmp\x00{}\x0030\x0010.0.0.1\x00" +
		"00000000-0000-0000-0000-000000000002\x00web\x00http\x00{\"url\":\"https://x\"}\x0060\x0010.0.0.2\x00"
	if got := string(encoding(goldenSpecs())); got != want {
		t.Fatalf("encoding:\n got %q\nwant %q", got, want)
	}
	const wantVersion = "276a113ca286305529b5bcb8c78ab04ea48bfdb7d5859163d4fb7a157cf07e17"
	sum := sha256.Sum256([]byte(want))
	if got := Version(goldenSpecs()); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("version %s is not the hash of the encoding", got)
	}
	if got := Version(goldenSpecs()); got != wantVersion {
		t.Fatalf("version = %s, want %s", got, wantVersion)
	}
}

func TestVersionOfTheEmptySetIsTheHashOfNothing(t *testing.T) {
	t.Parallel()
	const empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := Version(nil); got != empty {
		t.Fatalf("Version(nil) = %s, want %s", got, empty)
	}
	if got := Version([]Spec{}); got != empty {
		t.Fatalf("Version(empty) = %s, want %s", got, empty)
	}
}

func TestVersionIgnoresOrderAndKeepsTheInput(t *testing.T) {
	t.Parallel()
	in := goldenSpecs()
	reversed := []Spec{in[1], in[0]}
	if Version(in) != Version(reversed) {
		t.Fatal("version depends on input order")
	}
	if in[0].TargetHostID != targetTwo {
		t.Fatal("Version reordered its input")
	}
	// Same target, keys order the specs.
	a := Spec{TargetHostID: targetOne, CheckKey: "b", Plugin: "tcp", ParamsJSON: "{}", IntervalSeconds: 60}
	b := Spec{TargetHostID: targetOne, CheckKey: "a", Plugin: "tcp", ParamsJSON: "{}", IntervalSeconds: 60}
	if !strings.HasPrefix(string(encoding([]Spec{a, b})), targetOne.String()+"\x00a\x00") {
		t.Fatal("specs of one target are not ordered by check key")
	}
}

func TestVersionChangesWithEveryField(t *testing.T) {
	t.Parallel()
	base := goldenSpecs()[1]
	edits := map[string]func(*Spec){
		"target":   func(s *Spec) { s.TargetHostID = targetTwo },
		"key":      func(s *Spec) { s.CheckKey = "ping2" },
		"plugin":   func(s *Spec) { s.Plugin = "tcp" },
		"params":   func(s *Spec) { s.ParamsJSON = `{"count":3}` },
		"interval": func(s *Spec) { s.IntervalSeconds = 31 },
		"address":  func(s *Spec) { s.Address = "10.0.0.9" },
	}
	for name, edit := range edits {
		changed := base
		edit(&changed)
		if Version([]Spec{base}) == Version([]Spec{changed}) {
			t.Errorf("%s: version unchanged", name)
		}
	}
}

func TestProtoRoundTrip(t *testing.T) {
	t.Parallel()
	cfg := ToProto(goldenSpecs())
	if cfg.GetVersion() != Version(goldenSpecs()) {
		t.Fatalf("ToProto version = %s", cfg.GetVersion())
	}
	if len(cfg.GetProbes()) != 2 || cfg.GetProbes()[0].GetTargetHostId() != targetOne.String() {
		t.Fatalf("ToProto probes not in canonical order: %v", cfg.GetProbes())
	}
	b, err := proto.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var wire agentpb.ProbeConfig
	if err := proto.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	specs, err := FromProto(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if Version(specs) != cfg.GetVersion() {
		t.Fatal("specs read back hash to another version")
	}
	want := map[uuid.UUID]Spec{}
	for _, s := range goldenSpecs() {
		want[s.TargetHostID] = s
	}
	for _, s := range specs {
		if want[s.TargetHostID] != s {
			t.Errorf("spec %v, want %v", s, want[s.TargetHostID])
		}
	}
}

func TestFromProtoRefusesANonCanonicalTarget(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "nope", "{00000000-0000-0000-0000-000000000001}", "00000000-0000-0000-0000-00000000000A"} {
		_, err := FromProto(&agentpb.ProbeConfig{Probes: []*agentpb.ProbeSpec{{TargetHostId: id, CheckKey: "k", Plugin: "tcp"}}})
		if err == nil {
			t.Errorf("target %q accepted", id)
		}
	}
	specs, err := FromProto(nil)
	if err != nil || len(specs) != 0 {
		t.Fatalf("FromProto(nil) = %v, %v", specs, err)
	}
}
