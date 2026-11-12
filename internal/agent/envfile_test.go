package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    map[string]string
		wantErr string
	}{
		{"plain", "A=1\nB=2\n", map[string]string{"A": "1", "B": "2"}, ""},
		{"bom and crlf", "\uFEFFPULSIX_MANAGER_ADDR=m:8444\r\nX=y\r\n", map[string]string{"PULSIX_MANAGER_ADDR": "m:8444", "X": "y"}, ""},
		{"comments and blanks", "# c\n\n  \nK=a=b\n", map[string]string{"K": "a=b"}, ""},
		{"no quote stripping", "K=\"q\"\n", map[string]string{"K": "\"q\""}, ""},
		{"no value separator", "novalue\n", nil, "line 1"},
		{"empty key", "=v\n", nil, "line 1"},
		{"error names its line", "A=1\n\nbad\n", nil, "line 3"},
		{"no trailing newline", "A=1", map[string]string{"A": "1"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEnvFile(strings.NewReader(tt.in))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseEnvFile: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeEnvProcessWins(t *testing.T) {
	got := MergeEnv([]string{"A=env"}, map[string]string{"A": "file", "B": "file"})
	count := map[string]int{}
	for _, kv := range got {
		k, _, _ := strings.Cut(kv, "=")
		count[k]++
	}
	if count["A"] != 1 || count["B"] != 1 {
		t.Fatalf("got %v, want A and B once each", got)
	}
	has := func(s string) bool {
		for _, kv := range got {
			if kv == s {
				return true
			}
		}
		return false
	}
	if !has("A=env") || !has("B=file") {
		t.Errorf("got %v, want A=env and B=file", got)
	}
}
