// Package e2e hosts the milestone gate (backlog 2.12a, plan Task 14): the
// Ansible run driven end to end through the real HTTP API of a fully wired
// manager — enrollment, SSH credentials, host and bastion, connection test
// with host-key pinning, git repository sync, job template, the run itself
// with its live event stream, and the retry path — against in-process SSH
// and git fixtures. Only the pieces every scenario shares live here; the
// scenario itself is ansible_test.go (a test-only package: everything else
// in this directory is _test.go).
//
// The golden fixture (fixtures/inventory.golden.json) is the masked
// inventory render of the scenario's own scope, so it can never carry a
// revealed secret; the two helpers below are the contract that keeps it
// byte-stable across environments.
package e2e

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// GoldenPath is the checked-in golden fixture the scenario compares its
// render against.
const GoldenPath = "fixtures/inventory.golden.json"

// normalizeHost, normalizePort and normalizeBastion are the three known
// environment-dependent values of the render: the fixture SSH servers bind
// loopback addresses with dynamic ports, so the document carries them in
// three stable shapes. Everything else in the render is deterministic and
// compared byte for byte.
var (
	normalizeHost    = regexp.MustCompile(`"ansible_host":"[^"]*"`)
	normalizePort    = regexp.MustCompile(`"ansible_port":\d+`)
	normalizeBastion = regexp.MustCompile(`"ansible_ssh_common_args":"-o ProxyJump=[^"]*"`)
)

// Golden placeholders. ansible_port is normalized to the zero value to keep
// its JSON type intact; the two string values get named placeholders.
const (
	placeholderPort    = 0
	placeholderHost    = "{{HOST}}"
	placeholderBastion = "{{BASTION}}"
)

// NormalizeInventory renders one marshaled inventory document (Renderer.
// Preview's Doc, as served by GET /inventory/preview) into the
// deterministic form the golden fixture stores: the fixture servers'
// dynamic loopback addresses and ports become stable placeholders. The
// result is deterministic JSON — encoding/json sorts map keys — so the
// comparison against the checked-in fixture is exact.
func NormalizeInventory(raw json.RawMessage) (string, error) {
	// Re-marshal through a generic value first: the API serves the
	// document with whatever whitespace encoding/json produced at the
	// handler boundary, and the placeholders below are defined against
	// the compact, key-sorted form.
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("normalize inventory: document is not JSON: %w", err)
	}
	compact, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("normalize inventory: re-encode: %w", err)
	}
	out := normalizeHost.ReplaceAll(compact, []byte(`"ansible_host":"`+placeholderHost+`"`))
	out = normalizePort.ReplaceAll(out, []byte(`"ansible_port":`+fmt.Sprint(placeholderPort)))
	out = normalizeBastion.ReplaceAll(out, []byte(`"ansible_ssh_common_args":"`+placeholderBastion+`"`))
	return string(out), nil
}

// ParseEnrollmentBlocks splits `pulsix-manager worker add`'s stdout into
// its three PEM blocks in printed order — client certificate, private key,
// CA certificate — the exact contract writeEnrollmentOutput pins.
func ParseEnrollmentBlocks(out string) (certPEM, keyPEM, caPEM string, err error) {
	var blocks []string
	var cur []string
	var inBlock bool
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "-----BEGIN "):
			inBlock, cur = true, []string{line}
		case inBlock:
			cur = append(cur, line)
			if strings.HasPrefix(line, "-----END ") {
				blocks = append(blocks, strings.Join(cur, "\n"))
				inBlock = false
			}
		}
	}
	if len(blocks) != 3 {
		return "", "", "", fmt.Errorf("parse enrollment output: got %d PEM blocks, want 3 (cert, key, CA)", len(blocks))
	}
	return blocks[0], blocks[1], blocks[2], nil
}
