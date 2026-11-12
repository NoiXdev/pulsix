package agent

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// ParseEnvFile reads KEY=VALUE lines: blank lines and lines starting with
// '#' are ignored, there is no quoting and no expansion, and the value is
// everything after the first '='. A leading UTF-8 byte order mark and CRLF
// line ends are accepted (Windows editors write both). Errors name the
// 1-based line number.
func ParseEnvFile(r io.Reader) (map[string]string, error) {
	out := make(map[string]string)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if n == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("env file line %d: expected KEY=VALUE", n)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("env file line %d: empty variable name", n)
		}
		out[key] = value
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read env file: %w", err)
	}
	return out, nil
}

// MergeEnv returns env followed by every file entry whose key env does not
// already set: the process environment wins over the file.
func MergeEnv(env []string, file map[string]string) []string {
	set := make(map[string]bool, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		set[k] = true
	}
	out := make([]string, len(env), len(env)+len(file))
	copy(out, env)
	for k, v := range file {
		if !set[k] {
			out = append(out, k+"="+v)
		}
	}
	return out
}
