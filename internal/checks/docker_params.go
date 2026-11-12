package checks

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Bounds of the docker plugin's parameters (backlog 3.12). The agent trusts
// params that pass here, so these and internal/agent/checks/docker.go agree.
const (
	maxDockerLabelKeyLen   = 255
	maxDockerLabelValueLen = 255
	maxDockerSocketLen     = 255
	dockerPipePrefix       = `npipe:////./pipe/`
)

// dockerLabelKey is the shape of a container label key the plugin selects on.
var dockerLabelKey = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)

// validateDockerParams checks the docker plugin's parameters: selection
// (all or labeled), label (key or key=value) and socket (an absolute path or
// a Windows named pipe). Only the shape is checked here; what a label means
// under each selection is the agent's business, so a label next to
// selection "all" is accepted.
func validateDockerParams(params map[string]any) error {
	for k := range params {
		if k != "selection" && k != "label" && k != "socket" {
			return fmt.Errorf("%w: docker: unknown parameter %q", ErrInvalidInput, k)
		}
	}
	if v, ok := params["selection"]; ok {
		s, isStr := v.(string)
		if !isStr || (s != "all" && s != "labeled") {
			return fmt.Errorf("%w: docker: selection must be \"all\" or \"labeled\"", ErrInvalidInput)
		}
	}
	if v, ok := params["label"]; ok {
		s, isStr := v.(string)
		if !isStr {
			return fmt.Errorf("%w: docker: label must be a string", ErrInvalidInput)
		}
		if err := validateDockerLabel(s); err != nil {
			return err
		}
	}
	if v, ok := params["socket"]; ok {
		s, isStr := v.(string)
		if !isStr {
			return fmt.Errorf("%w: docker: socket must be a string", ErrInvalidInput)
		}
		if err := validateDockerSocket(s); err != nil {
			return err
		}
	}
	return nil
}

func validateDockerLabel(s string) error {
	key, value, _ := strings.Cut(s, "=")
	if !dockerLabelKey.MatchString(key) {
		return fmt.Errorf("%w: docker: label key must be 1 to %d characters of letters, digits, '.', '_', '/' or '-'", ErrInvalidInput, maxDockerLabelKeyLen)
	}
	if len(value) > maxDockerLabelValueLen {
		return fmt.Errorf("%w: docker: label value must be at most %d bytes", ErrInvalidInput, maxDockerLabelValueLen)
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("%w: docker: label value must not contain control characters", ErrInvalidInput)
	}
	return nil
}

func validateDockerSocket(s string) error {
	if len(s) > maxDockerSocketLen {
		return fmt.Errorf("%w: docker: socket must be at most %d bytes", ErrInvalidInput, maxDockerSocketLen)
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return fmt.Errorf("%w: docker: socket must not contain control characters", ErrInvalidInput)
	}
	isPath := strings.HasPrefix(s, "/")
	isPipe := strings.HasPrefix(s, dockerPipePrefix) && len(s) > len(dockerPipePrefix)
	if !isPath && !isPipe {
		return fmt.Errorf("%w: docker: socket must be an absolute path or %s followed by a pipe name", ErrInvalidInput, dockerPipePrefix)
	}
	return nil
}
