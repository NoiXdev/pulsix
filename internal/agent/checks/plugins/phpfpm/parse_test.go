package phpfpm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseFixtures(t *testing.T) {
	tests := []struct {
		file string
		want status
	}{
		{"php-7.4.json", status{startTime: 1699866000, accepted: 1520, listenQueue: 1, idle: 3, active: 1, total: 4, maxChildren: 2, slow: 4}},
		{"php-8.3.json", status{startTime: 1794561026, accepted: 2, listenQueue: 0, idle: 1, active: 1, total: 2}},
		// full lists every process (with fractional numbers of its own):
		// ignored.
		{"php-8.3-full.json", status{startTime: 1794561026, accepted: 4, listenQueue: 0, idle: 1, active: 1, total: 2}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got, err := parseStatus([]byte(fixture(t, tt.file)))
			if err != nil || got != tt.want {
				t.Fatalf("parseStatus = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

// PHP 8.4 adds "memory peak"; unknown keys are ignored, as are the
// optional ones a page may lack.
func TestParseIgnoresUnknownAndOptionalKeys(t *testing.T) {
	page := `{"start time":5,"accepted conn":6,"listen queue":0,"idle processes":1,"active processes":2,` +
		`"total processes":3,"max children reached":0,"slow requests":0,"memory peak":2097152,"new key":[1,{"x":null}]}`
	got, err := parseStatus([]byte(page))
	if err != nil || got != (status{startTime: 5, accepted: 6, idle: 1, active: 2, total: 3}) {
		t.Fatalf("parseStatus = %+v, %v", got, err)
	}
}

func TestParseRefusesWhatIsNotTheFormat(t *testing.T) {
	good := fixture(t, "php-8.3.json")
	tests := []struct{ name, body string }{
		{"empty", ""},
		{"html", "<html>pool: www</html>"},
		{"text status page", "pool:                 www\nprocess manager:      dynamic\n"},
		{"array", "[" + good + "]"},
		{"null", "null"},
		{"empty object", "{}"},
		{"trailing data", good + "{}"},
		{"string number", strings.Replace(good, `"accepted conn":2`, `"accepted conn":"2"`, 1)},
		{"negative", strings.Replace(good, `"listen queue":0`, `"listen queue":-1`, 1)},
		{"fraction", strings.Replace(good, `"idle processes":1`, `"idle processes":1.5`, 1)},
		{"exponent", strings.Replace(good, `"total processes":2`, `"total processes":2e0`, 1)},
		{"null value", strings.Replace(good, `"slow requests":0`, `"slow requests":null`, 1)},
		{"bool value", strings.Replace(good, `"max children reached":0`, `"max children reached":false`, 1)},
		{"overflow", strings.Replace(good, `"accepted conn":2`, `"accepted conn":18446744073709551616`, 1)},
		{"pool not a string", strings.Replace(good, `"pool":"www"`, `"pool":1`, 1)},
		{"optional key of a wrong type", strings.Replace(good, `"max listen queue":0`, `"max listen queue":"0"`, 1)},
		{"deep nesting", strings.Repeat("[", 20000) + strings.Repeat("]", 20000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseStatus([]byte(tt.body)); err != errFormat {
				t.Fatalf("parseStatus = %v, want errFormat", err)
			}
		})
	}
}

func TestParseNeedsEveryRequiredKey(t *testing.T) {
	good := fixture(t, "php-8.3.json")
	for _, key := range []string{"start time", "accepted conn", "listen queue", "idle processes", "active processes",
		"total processes", "max children reached", "slow requests"} {
		t.Run(key, func(t *testing.T) {
			renamed := strings.Replace(good, `"`+key+`"`, `"x `+key+`"`, 1)
			if renamed == good {
				t.Fatalf("fixture has no %q", key)
			}
			if _, err := parseStatus([]byte(renamed)); err != errFormat {
				t.Fatalf("parseStatus without %q = %v, want errFormat", key, err)
			}
		})
	}
	// The largest counter FPM can report is taken as it is.
	max := strings.Replace(good, `"accepted conn":2`, `"accepted conn":18446744073709551615`, 1)
	if s, err := parseStatus([]byte(max)); err != nil || s.accepted != 1<<64-1 {
		t.Fatalf("parseStatus(max uint64) = %+v, %v", s, err)
	}
}
