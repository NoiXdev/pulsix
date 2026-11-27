package secrets

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// secretValue is planted in every malformed file below: no error text may
// ever contain it (Review Focus 1 — a parser error must not quote a line).
const secretValue = "s3cret-Value-42"

func TestParseAcceptsTheDocumentedFormat(t *testing.T) {
	src := "\uFEFF# agent.yaml\r\n" + `secrets:
  pg_main:
    dsn: "postgres://monitor:s3cret@localhost:5432/postgres?sslmode=disable"
  mysql_shop:
    user: monitor
    password: 's3cret'
    host: 127.0.0.1
    port: 3306
    tls: true
    ratio: 0.5
    empty: ""
  redis.cache-1:
    password: |
      multi
      line
  Empty: {}
`
	set, err := parse([]byte(src))
	if err != nil {
		t.Fatalf("parse = %v", err)
	}
	if got, want := set.Names(), []string{"Empty", "mysql_shop", "pg_main", "redis.cache-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}
	tests := []struct {
		name string
		want map[string]string
	}{
		{"pg_main", map[string]string{"dsn": "postgres://monitor:s3cret@localhost:5432/postgres?sslmode=disable"}},
		// Unquoted numbers and booleans are taken as written: an operator
		// writing port: 3306 means the text "3306".
		{"mysql_shop", map[string]string{
			"user": "monitor", "password": "s3cret", "host": "127.0.0.1",
			"port": "3306", "tls": "true", "ratio": "0.5", "empty": "",
		}},
		{"redis.cache-1", map[string]string{"password": "multi\nline\n"}},
		{"Empty", map[string]string{}},
	}
	for _, tt := range tests {
		got, ok := set.Fields(tt.name)
		if !ok || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Fields(%q) = %v, %v; want %v", tt.name, got, ok, tt.want)
		}
	}
	if _, ok := set.Fields("nope"); ok {
		t.Error("Fields of an undefined secret reported ok")
	}
}

func TestParseEmptyFilesHoldNoSecrets(t *testing.T) {
	for _, src := range []string{"", "# only a comment\n", "secrets:\n", "secrets: {}\n", "---\nsecrets:\n"} {
		set, err := parse([]byte(src))
		if err != nil {
			t.Errorf("parse(%q) = %v", src, err)
			continue
		}
		if set.Len() != 0 {
			t.Errorf("parse(%q) has %d secrets", src, set.Len())
		}
	}
}

func TestParseRefusesWithoutQuotingTheFile(t *testing.T) {
	long := strings.Repeat("x", maxValueBytes+1)
	tests := []struct {
		name string
		src  string
		want string // the exact error text: line, column and a generic reason
	}{
		{"unknown top-level key", "secrets: {}\nrules:\n  - " + secretValue + "\n",
			"line 2, column 1: unknown top-level key; only secrets, commands, checks and probes are allowed"},
		{"top level not a mapping", "- " + secretValue + "\n",
			"line 1, column 1: the file must be a mapping with a secrets, commands, checks or probes key"},
		{"secrets not a mapping", "secrets:\n  - " + secretValue + "\n",
			"line 2, column 3: secrets must map secret names to their fields"},
		{"secret not a mapping", "secrets:\n  pg: " + secretValue + "\n",
			"line 2, column 7: a secret must map field names to values"},
		{"secret null", "secrets:\n  pg:\n",
			"line 2, column 3: a secret must map field names to values"},
		{"bad secret name", "secrets:\n  \"" + secretValue + " x\":\n    a: b\n",
			"line 2, column 3: a secret name is 1 to 64 characters of A-Z a-z 0-9 _ . -"},
		{"empty secret name", "secrets:\n  \"\":\n    a: b\n",
			"line 2, column 3: a secret name is 1 to 64 characters of A-Z a-z 0-9 _ . -"},
		{"long secret name", "secrets:\n  " + strings.Repeat("a", 65) + ":\n    a: b\n",
			"line 2, column 3: a secret name is 1 to 64 characters of A-Z a-z 0-9 _ . -"},
		{"bad field name", "secrets:\n  pg:\n    \"" + secretValue + "=x\": y\n",
			"line 3, column 5: a field name is 1 to 64 characters of A-Z a-z 0-9 _ . -"},
		{"duplicate secret", "secrets:\n  pg:\n    a: b\n  pg:\n    a: " + secretValue + "\n",
			"line 4, column 3: this secret is already defined at line 2"},
		{"duplicate field", "secrets:\n  pg:\n    password: x\n    password: " + secretValue + "\n",
			"line 4, column 5: this field is already set at line 3"},
		{"duplicate top-level key", "secrets: {}\nsecrets: {}\n",
			"line 2, column 1: secrets is already set at line 1"},
		{"field null", "secrets:\n  pg:\n    password:\n",
			`line 3, column 14: a field has no value; write "" for an empty one`},
		{"field a mapping", "secrets:\n  pg:\n    password:\n      x: " + secretValue + "\n",
			"line 4, column 7: a field value must be a single value, not a list or mapping"},
		{"field a list", "secrets:\n  pg:\n    password: [" + secretValue + "]\n",
			"line 3, column 15: a field value must be a single value, not a list or mapping"},
		// Anchors are refused wherever they stand, with or without an
		// alias that uses them, and the anchor's name is never quoted.
		{"alias", "secrets:\n  pg:\n    a: &x " + secretValue + "\n    b: *x\n",
			"line 3, column 8: " + badAnchor},
		{"anchor on a value", "secrets:\n  pg:\n    password: &" + secretValue + " x\n",
			"line 3, column 15: " + badAnchor},
		{"anchor on a secret", "secrets:\n  pg: &" + secretValue + "\n    password: x\n",
			"line 2, column 7: " + badAnchor},
		{"anchor on a secret name", "secrets:\n  &" + secretValue + " pg:\n    password: x\n",
			"line 2, column 3: " + badAnchor},
		{"anchor on the file", "&" + secretValue + "\nsecrets:\n  pg:\n    password: x\n",
			"line 1, column 1: " + badAnchor},
		{"value too long", "secrets:\n  pg:\n    password: " + long + "\n",
			"line 3, column 15: a field value is longer than 4096 bytes"},
		{"complex key", "secrets:\n  ? [a, b]\n  : x\n",
			"line 2, column 5: a secret name is 1 to 64 characters of A-Z a-z 0-9 _ . -"},
		{"two documents", "secrets: {}\n---\nsecrets:\n  pg:\n    a: " + secretValue + "\n",
			"line 2, column 1: the file holds more than one YAML document"},
		// Syntax errors: yaml reports a line only (for some problems the
		// line where the enclosing block starts); a known reason is kept.
		{"syntax: indentation", "secrets:\n  pg:\n    password: " + secretValue + "\n   user: x\n",
			"line 1: not valid YAML (did not find expected key)"},
		{"syntax: mapping in a scalar", "secrets:\n  pg:\n    password: " + secretValue + ": x\n",
			"line 3: not valid YAML (mapping values are not allowed in this context)"},
		{"syntax: bad token", "secrets:\n  pg:\n    password: `" + secretValue + "\n",
			"line 3: not valid YAML (found character that cannot start any token)"},
		// An unknown anchor's error quotes the anchor name, which comes
		// from the file, and carries no line: it is replaced by fixed text.
		{"syntax: unknown anchor", "secrets:\n  pg:\n    password: *" + secretValue + "\n",
			"not valid YAML (an alias refers to an unknown anchor)"},
		{"syntax: unterminated quote", "secrets:\n  pg:\n    password: \"" + secretValue + "\n",
			"line 3: not valid YAML (found unexpected end of stream)"},
		// A problem not on the allowlist keeps its line and nothing else.
		{"syntax: unlisted problem", "secrets:\n  pg:\n    password: |0\n      " + secretValue + "\n",
			"line 3: not valid YAML"},
		{"syntax: unlisted problem without a line", "%FOO " + secretValue + "\n---\nsecrets: {}\n",
			"not valid YAML"},
		// Explicit tags: only !!str on a value, which changes nothing.
		{"tag: binary", "secrets:\n  pg:\n    password: !!binary " + secretValue + "\n",
			"line 3, column 15: " + badTag},
		{"tag: custom", "secrets:\n  pg:\n    password: !vault " + secretValue + "\n",
			"line 3, column 15: " + badTag},
		{"tag: int", "secrets:\n  pg:\n    port: !!int 5432\n",
			"line 3, column 11: " + badTag},
		{"tag: null", "secrets:\n  pg:\n    password: !!null\n",
			"line 3, column 15: " + badTag},
		{"tag: on a secret", "secrets:\n  pg: !!map\n    password: " + secretValue + "\n",
			"line 2, column 7: " + badTag},
		{"tag: str on a secret", "secrets:\n  pg: !!str\n    password: " + secretValue + "\n",
			"line 2, column 7: " + badTag},
		{"tag: on a secret name", "secrets:\n  !!int 5:\n    password: " + secretValue + "\n",
			"line 2, column 3: " + badTag},
		{"tag: on the file", "!!map\nsecrets:\n  pg:\n    password: " + secretValue + "\n",
			"line 1, column 1: " + badTag},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, err := parse([]byte(tt.src))
			if err == nil {
				t.Fatalf("parse accepted %q: %v", tt.src, set.Names())
			}
			if strings.Contains(err.Error(), secretValue) || strings.Contains(err.Error(), "xxxxxxxx") {
				t.Fatalf("error quotes the file: %q", err)
			}
			if err.Error() != tt.want {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
		})
	}
}

// badTag is the reason given for an explicit tag other than !!str.
const badTag = "YAML tags are not supported, except !!str on a value"

// badAnchor is the reason given for any anchor (and so for any alias).
const badAnchor = "YAML anchors and aliases are not supported"

func TestParseAcceptsTheStrTag(t *testing.T) {
	set, err := parse([]byte("secrets:\n  !!str pg:\n    !!str port: !!str 5432\n    tls: ! true\n"))
	if err != nil {
		t.Fatalf("parse = %v", err)
	}
	if f, ok := set.Fields("pg"); !ok || !reflect.DeepEqual(f, map[string]string{"port": "5432", "tls": "true"}) {
		t.Fatalf("Fields(pg) = %v, %v", f, ok)
	}
}

func TestParseLimitsTheNumberOfSecrets(t *testing.T) {
	var b strings.Builder
	b.WriteString("secrets:\n")
	for i := 0; i < maxSecrets; i++ {
		b.WriteString("  s" + strconv.Itoa(i) + ":\n    a: b\n")
	}
	if set, err := parse([]byte(b.String())); err != nil || set.Len() != maxSecrets {
		t.Fatalf("parse(%d secrets) = %d, %v", maxSecrets, set.Len(), err)
	}
	b.WriteString("  one_more:\n    a: b\n")
	_, err := parse([]byte(b.String()))
	if err == nil || err.Error() != "line 514, column 3: more than 256 secrets" {
		t.Fatalf("parse(%d secrets) = %v", maxSecrets+1, err)
	}
}

func TestParseAcceptsAValueOfExactlyTheLimit(t *testing.T) {
	v := strings.Repeat("y", maxValueBytes)
	set, err := parse([]byte("secrets:\n  pg:\n    password: " + v + "\n"))
	if err != nil {
		t.Fatalf("parse = %v", err)
	}
	if f, _ := set.Fields("pg"); f["password"] != v {
		t.Fatal("value not kept")
	}
}
