package ansiblevars_test

import (
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/ansiblevars"
)

func TestForbiddenInUserVars(t *testing.T) {
	cases := []struct {
		name      string
		forbidden bool
	}{
		{"ansible_host_key_checking", true},
		{"ansible_ssh_host_key_checking", true},
		{"ansible_ssh_args", true},
		{"ansible_ssh_common_args", true},
		{"ansible_ssh_extra_args", true},
		{"ansible_scp_extra_args", true},
		{"ansible_sftp_extra_args", true},
		{"ansible_ssh_executable", true},
		{"ansible_connection", true},
		{"ansible_paramiko_host_key_checking", true},
		{"ansible_paramiko_look_for_keys", true},
		{"ANSIBLE_SSH_ARGS", true}, // variable names are case-sensitive in ansible, refused anyway
		{"pulsix_ssh_common_args", true},
		{"pulsix_anything", true},
		{"ansible_become_password", false},
		{"ansible_python_interpreter", false},
		{"greeting", false},
	}
	for _, c := range cases {
		if got := ansiblevars.Forbidden(c.name); got != c.forbidden {
			t.Errorf("Forbidden(%q) = %v, want %v", c.name, got, c.forbidden)
		}
	}
	got := ansiblevars.ForbiddenIn(map[string]any{"ansible_ssh_args": "-o StrictHostKeyChecking=no", "greeting": "hi", "ansible_host_key_checking": false})
	if !slices.Equal(got, []string{"ansible_host_key_checking", "ansible_ssh_args"}) {
		t.Fatalf("ForbiddenIn = %v", got)
	}
}
