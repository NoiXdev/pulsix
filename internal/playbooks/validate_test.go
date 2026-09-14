package playbooks

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateYAML covers the brief's table: the validator is the only gate
// between an operator's editor text and a stored playbook (the worker's
// pre-run syntax check remains the real gate for plays after the first —
// see ValidateYAML's doc comment), so each failure mode must name its
// cause in a stable, human-readable way.
func TestValidateYAML(t *testing.T) {
	cases := []struct {
		name    string
		inline  string
		wantErr string // exact message fragment; "" means valid
	}{
		{"valid single play", "- hosts: all\n  tasks: []\n", ""},
		{"valid multi-play with vars", "- hosts: web\n  vars:\n    x: 1\n  tasks: []\n- hosts: db\n  tasks: []\n", ""},
		{"not a list", "hosts: all\n", "expected a list of plays"},
		{"scalar document", "42\n", "expected a list of plays"},
		{"empty document", "", "expected a list of plays"},
		{"empty list", "[]\n", "expected a list of plays"},
		{"first play not a mapping", "- x\n", "play 1: must be a mapping"},
		{"missing hosts", "- name: x\n  tasks: []\n", "play 1: missing hosts"},
		{"empty hosts", "- hosts: \"\"\n  tasks: []\n", "play 1: missing hosts"},
		{"hosts not a string", "- hosts: 123\n  tasks: []\n", "play 1: hosts must be a string"},
		{"malformed yaml", "- hosts: all\n\ttasks: []\n", "malformed YAML: "},
		// yaml.v3 (like Ansible's own loader) considers the first document
		// only: the trailing document is ignored, not an error.
		{"multiple documents", "- hosts: all\n---\ngarbage\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateYAML(tc.inline)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateYAML(%q) = %v, want valid", tc.inline, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ValidateYAML(%q) = %v, want ErrInvalidInput", tc.inline, err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateYAML(%q) = %q, want message containing %q", tc.inline, err, tc.wantErr)
			}
		})
	}
}

// TestValidateYAMLSizeLimit pins the 512 KiB boundary: exactly the limit is
// stored, one byte over is rejected with the exact message.
func TestValidateYAMLSizeLimit(t *testing.T) {
	// A comment-padded single play reaches the byte counts precisely.
	atLimit := "- hosts: all\n# " + strings.Repeat("x", 512*1024-len("- hosts: all\n# ")-1) + "\n"
	if len(atLimit) != 512*1024 {
		t.Fatalf("test fixture is %d bytes, want exactly 524288", len(atLimit))
	}
	if err := ValidateYAML(atLimit); err != nil {
		t.Fatalf("a playbook of exactly 512 KiB must be accepted: %v", err)
	}
	if err := ValidateYAML(atLimit + "x"); !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "playbook exceeds 512 KiB") {
		t.Fatalf("one byte over the limit = %v, want the exact size message", err)
	}
}
