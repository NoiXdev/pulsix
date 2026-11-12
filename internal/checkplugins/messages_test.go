package checkplugins_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestValidateMessages pins what a refusal says: the plugin, the parameter
// and the rule, in the words internal/checks used before the descriptors
// (backlog 3.54) wherever those were already precise. The text never
// repeats ErrInvalidParams', so a caller wrapping it into its own sentinel
// reads "invalid input: disk: …", as before.
func TestValidateMessages(t *testing.T) {
	cases := []struct {
		plugin string
		params map[string]any
		want   string
	}{
		{"ping", nil, `unsupported plugin "ping"`},
		{"cpu", map[string]any{"x": 1}, `plugin "cpu" takes no parameters`},
		{"disk", map[string]any{"typo": []any{"/"}}, `disk: unknown parameter "typo"`},
		{"disk", map[string]any{"mounts": "/"}, "disk: mounts must be a list of strings"},
		{"disk", map[string]any{"mounts": []any{""}}, "disk: mounts must not contain an empty entry"},
		{"disk", map[string]any{"mounts": []any{" "}}, "disk: mounts entries must not be blank"},
		{"service", nil, "service: units is required"},
		{"service", map[string]any{"units": []any{}}, "service: units must have at least one entry"},
		{"temperature", map[string]any{"sensors": anyList(65, "s")}, "temperature: sensors must have at most 64 entries"},
		{"temperature", map[string]any{"sensors": []any{strings.Repeat("s", 256)}}, "temperature: sensors entries must be at most 255 bytes"},
		{"raid", map[string]any{"arrays": []any{"md\x000"}}, "raid: arrays entries must not contain control characters"},
		{"smart", map[string]any{"devices": []any{"/dev/sda\n"}}, "smart: devices entries must not contain control characters"},
		{"smart", map[string]any{"devices": []any{"sda"}}, "smart: devices entries must start with /dev/ and contain only letters, digits and . _ / , : + @ -"},
		{"smart", map[string]any{"devices": []any{"/dev/../x"}}, "smart: devices entries must not contain a .. path segment"},
		{"docker", map[string]any{"selection": "some"}, "docker: selection must be one of all, labeled"},
		{"docker", map[string]any{"label": 1}, "docker: label must be a string"},
		{"docker", map[string]any{"label": "bad key"}, "docker: label key must be 1 to 255 characters of letters, digits, '.', '_', '/' or '-'"},
		{"docker", map[string]any{"label": "k=" + strings.Repeat("v", 256)}, "docker: label value must be at most 255 bytes"},
		{"docker", map[string]any{"label": "k=a\nb"}, "docker: label value must not contain control characters"},
		{"docker", map[string]any{"socket": "/" + strings.Repeat("s", 255)}, "docker: socket must be at most 255 bytes"},
		{"docker", map[string]any{"socket": "/a\x00b"}, "docker: socket must not contain control characters"},
		{"docker", map[string]any{"socket": "docker.sock"}, "docker: socket must be an absolute path or npipe:////./pipe/ followed by a pipe name"},
		{"icmp", map[string]any{"count": 11.0}, "icmp: count must be a whole number between 1 and 10"},
		{"tcp", nil, "tcp: port is required"},
		{"tcp", map[string]any{"port": 22.0, "expect": ""}, "tcp: expect must not be empty"},
		{"tcp", map[string]any{"port": 22.0, "expect": strings.Repeat("x", 501)}, "tcp: expect must be at most 500 bytes"},
		{"http", map[string]any{"url": "ftp://x"}, "http: url must be an http:// or https:// URL with a host and no user info"},
		{"http", map[string]any{"url": "http://x", "verify_tls": 1.0}, "http: verify_tls must be true or false"},
		{"tls", map[string]any{"server_name": " "}, "tls: server_name must not be blank"},
		{"dns", map[string]any{"name": "a..b"}, "dns: name must be a DNS name: dot-separated labels of letters, digits, hyphens and underscores, at most 253 bytes (it may contain {address})"},
		{"dns", map[string]any{"server": "x"}, "dns: server must be an IP address with an optional port (an IPv6 address with a port in brackets)"},
		// Several faults name the first parameter in sorted order, so the same
		// input always gets the same message.
		{"icmp", map[string]any{"timeout_s": 0.0, "count": 0.0}, "icmp: count must be a whole number between 1 and 10"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			err := checkplugins.Validate(tc.plugin, tc.params)
			if !errors.Is(err, checkplugins.ErrInvalidParams) {
				t.Fatalf("Validate(%s, %v) = %v, want ErrInvalidParams", tc.plugin, tc.params, err)
			}
			if err.Error() != tc.want {
				t.Fatalf("Validate(%s, %v) = %q, want %q", tc.plugin, tc.params, err.Error(), tc.want)
			}
		})
	}
}
