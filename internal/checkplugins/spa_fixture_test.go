package checkplugins_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// spaFixture is the SPA's copy of GET /check-plugins' body. Its tests serve
// it as that route (web/src/test/mockApi.ts) and check every parameter in
// it has an English and a German label.
const spaFixture = "../../web/src/test/checkPlugins.json"

var updateSPAFixture = flag.Bool("update-spa-fixture", false, "rewrite "+spaFixture+" from the descriptors")

// TestSPAFixtureIsCurrent: the fixture is exactly the route's body —
// {"plugins": All()}, which internal/api's TestCheckPluginsServesEveryDescriptor
// pins the route to. A descriptor change fails here until the fixture is
// regenerated:
//
//	go test ./internal/checkplugins -run SPAFixture -update-spa-fixture
func TestSPAFixtureIsCurrent(t *testing.T) {
	want, err := json.MarshalIndent(map[string]any{"plugins": checkplugins.All()}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if *updateSPAFixture {
		if err := os.WriteFile(spaFixture, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(spaFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; regenerate it with go test ./internal/checkplugins -run SPAFixture -update-spa-fixture", spaFixture)
	}
}
