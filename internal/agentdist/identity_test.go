package agentdist

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The identity files the installers look for are the ones the agent loads
// (internal/agent/config.go) and needs to start without enrolling. A name
// changing there must change the installers with it (backlog 3.60).
var identityFiles = []string{"agent.crt", "agent.key", "agent.ca.pem"}

func TestInstallersNameTheAgentsIdentityFiles(t *testing.T) {
	config, err := os.ReadFile(filepath.Join("..", "agent", "config.go"))
	if err != nil {
		t.Fatalf("read the agent's config.go: %v", err)
	}
	for _, name := range identityFiles {
		if !strings.Contains(string(config), `"`+name+`"`) {
			t.Errorf("internal/agent/config.go no longer names %s; update has_identity and Test-IdentityPresent", name)
		}
	}
	sh, ps := renderReal(t), renderPowerShellReal(t)
	for _, name := range identityFiles {
		if !strings.Contains(sh, name) {
			t.Errorf("install.sh does not check for %s", name)
		}
		if !strings.Contains(ps, "'"+name+"'") {
			t.Errorf("install.ps1 does not check for %s", name)
		}
	}
}

// installFunction cuts the named top-level function out of the rendered
// installer, so a test can run it on its own: the installer itself needs
// root and a service manager.
func installFunction(t *testing.T, script, name string) string {
	t.Helper()
	start := strings.Index(script, "\n"+name+"() {\n")
	if start < 0 {
		t.Fatalf("install.sh has no function %s", name)
	}
	end := strings.Index(script[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("function %s is not closed", name)
	}
	return script[start : start+end+3]
}

func TestShellHasIdentity(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this system")
	}
	fn := installFunction(t, renderReal(t), "has_identity")

	// dir builds a data directory holding the named identity files with
	// the given contents.
	dir := func(t *testing.T, files map[string]string) string {
		t.Helper()
		d := t.TempDir()
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	full := func() map[string]string {
		return map[string]string{"agent.crt": "cert", "agent.key": "key", "agent.ca.pem": "ca"}
	}
	without := func(name string) map[string]string {
		m := full()
		delete(m, name)
		return m
	}
	empty := func(name string) map[string]string {
		m := full()
		m[name] = ""
		return m
	}
	withMeta := full()
	withMeta["agent.json"] = "{}"

	tests := []struct {
		name  string
		setup func(t *testing.T) string
		want  bool
	}{
		{"all three files", func(t *testing.T) string { return dir(t, full()) }, true},
		{"with agent.json too", func(t *testing.T) string { return dir(t, withMeta) }, true},
		{"empty directory", func(t *testing.T) string { return dir(t, nil) }, false},
		{"directory does not exist", func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing") }, false},
		{"no certificate", func(t *testing.T) string { return dir(t, without("agent.crt")) }, false},
		{"no key", func(t *testing.T) string { return dir(t, without("agent.key")) }, false},
		{"no CA chain", func(t *testing.T) string { return dir(t, without("agent.ca.pem")) }, false},
		{"empty certificate", func(t *testing.T) string { return dir(t, empty("agent.crt")) }, false},
		{"empty key", func(t *testing.T) string { return dir(t, empty("agent.key")) }, false},
		{"empty CA chain", func(t *testing.T) string { return dir(t, empty("agent.ca.pem")) }, false},
		{"only agent.json", func(t *testing.T) string { return dir(t, map[string]string{"agent.json": "{}"}) }, false},
		{"a directory in place of a file", func(t *testing.T) string {
			d := dir(t, without("agent.key"))
			if err := os.Mkdir(filepath.Join(d, "agent.key"), 0o700); err != nil {
				t.Fatal(err)
			}
			return d
		}, false},
		{"a symlink in place of a file", func(t *testing.T) string {
			d := dir(t, without("agent.key"))
			target := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(target, []byte("key"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(d, "agent.key")); err != nil {
				t.Skipf("no symlinks here: %v", err)
			}
			return d
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script := "set -eu\n" + fn + "\nhas_identity \"$1\"\n"
			err := exec.Command(sh, "-c", script, "sh", tc.setup(t)).Run()
			if got := err == nil; got != tc.want {
				t.Errorf("has_identity = %v, want %v (err %v)", got, tc.want, err)
			}
		})
	}
}

// The token handling around the identity check, in the order that makes
// it safe: an explicit token is read as before, only a missing one falls
// back to the identity and then to the prompt, and the env file leaves the
// token out only when the identity was reused.
func TestInstallScriptTokenIsOptionalOverAnIdentity(t *testing.T) {
	out := renderReal(t)
	for _, want := range []string{
		`PULSIX_REUSE_IDENTITY=""`,
		`if has_identity "$DATA_DIR"; then`,
		"pulsix-agent: reusing the existing identity in $DATA_DIR; no enrollment token needed (if it turns out to be expired or invalid, run the installer again with --token or --token-file)",
		`if [ -z "$PULSIX_REUSE_IDENTITY" ]; then
	[ -n "$PULSIX_TOKEN" ] || die "no enrollment token given"
fi`,
		`if [ -z "$PULSIX_REUSE_IDENTITY" ]; then
		echo "PULSIX_ENROLL_TOKEN=$PULSIX_TOKEN"
	fi`,
		"(the token is optional on a re-run over an existing identity)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered installer must contain %q", want)
		}
	}
	// The reuse only happens in the branch where no token was given.
	_, section, ok := strings.Cut(out, `PULSIX_REUSE_IDENTITY=""`)
	if !ok {
		t.Fatal("no token section")
	}
	section, _, _ = strings.Cut(section, `[ -n "$PULSIX_TOKEN" ] || die`)
	if got := strings.Count(section, "PULSIX_REUSE_IDENTITY=1"); got != 1 {
		t.Fatalf("the identity is reused in %d places, want 1", got)
	}
	explicit, _, _ := strings.Cut(section, "elif")
	if strings.Contains(explicit, "has_identity") || strings.Contains(explicit, "PULSIX_REUSE_IDENTITY=1") {
		t.Error("a given --token-file must not consult the identity")
	}
	if !strings.Contains(section, `elif [ -z "$PULSIX_TOKEN" ]; then`) {
		t.Error("only a missing --token may fall back to the identity")
	}
}

func TestInstallPs1TokenIsOptionalOverAnIdentity(t *testing.T) {
	script := string(PowerShellInstallScript())
	for _, want := range []string{
		"function Test-IdentityPresent([string]$Dir) {",
		"$reuseIdentity = $false",
		"if (Test-IdentityPresent $DataDir) {",
		"pulsix-agent: reusing the existing identity in $DataDir; no enrollment token needed (if it turns out to be expired or invalid, run the installer again with -Token or -TokenFile)",
		"if ((-not $reuseIdentity) -and (-not $Token)) {\n\tStop-Install 'no enrollment token given'",
		"$lines = @(\"PULSIX_MANAGER_ADDR=$Manager\")\n\tif (-not $reuseIdentity) {\n\t\t$lines += \"PULSIX_ENROLL_TOKEN=$Token\"\n\t}",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("install.ps1 must contain %q", want)
		}
	}
	// A given -TokenFile or -Token never consults the identity.
	_, section, ok := strings.Cut(script, "# 5. The token.")
	if !ok {
		t.Fatal("no token step")
	}
	section, _, _ = strings.Cut(section, "# 6.")
	explicit, _, _ := strings.Cut(section, "} elseif (-not $Token) {")
	if strings.Contains(explicit, "Test-IdentityPresent") {
		t.Error("a given -TokenFile must not consult the identity")
	}
	if !strings.Contains(section, "} elseif (-not $Token) {\n\tif (Test-IdentityPresent $DataDir) {") {
		t.Error("only a missing -Token may fall back to the identity")
	}
}
