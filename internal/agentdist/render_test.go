package agentdist

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func renderReal(t *testing.T) string {
	t.Helper()
	out, err := RenderShellInstaller("https://manager.example/")
	if err != nil {
		t.Fatalf("RenderShellInstaller: %v", err)
	}
	return string(out)
}

func TestRenderShellInstallerEmbedsUnitAndPlist(t *testing.T) {
	out := renderReal(t)
	for _, want := range []string{
		`BASE_URL="https://manager.example"`,
		"ExecStart=/usr/local/bin/pulsix-agent",
		"<string>dev.noix.pulsix-agent</string>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered installer must contain %q", want)
		}
	}
	if strings.Contains(out, "{{") || strings.Contains(out, "}}") {
		t.Error("the rendered installer still carries template syntax")
	}
}

// The unit and the plist are inlined into quoted heredocs: a body carrying
// its delimiter on a line of its own would close the heredoc early and hand
// the rest to the installing host's shell. Each is checked against its own.
func TestRenderShellInstallerRefusesADelimiterInTheUnitOrPlist(t *testing.T) {
	script, unit, plist := InstallScript(), Unit(), Plist()
	if _, err := renderShellInstaller(script, unit, plist, "https://m.example"); err != nil {
		t.Fatalf("render of the real files failed: %v", err)
	}
	if _, err := renderShellInstaller(script, []byte("[Service]\nPULSIX_UNIT_EOF\nrm -rf /\n"), plist, "https://m.example"); err == nil {
		t.Error("render accepted a unit that closes the heredoc")
	}
	if _, err := renderShellInstaller(script, unit, []byte("<plist>\nPULSIX_PLIST_EOF\nrm -rf /\n"), "https://m.example"); err == nil {
		t.Error("render accepted a plist that closes the heredoc")
	}
	// Each file is only checked against its own delimiter, and only on a line of its own.
	if _, err := renderShellInstaller(script, unit, []byte("<plist>PULSIX_UNIT_EOF</plist>\n"), "https://m.example"); err != nil {
		t.Errorf("a delimiter inside a line is harmless: %v", err)
	}
}

func TestInstallScriptKnowsDarwin(t *testing.T) {
	out := renderReal(t)
	for _, want := range []string{
		"Darwin)",
		"Linux)",
		"shasum -a 256",
		"stat -f",
		"stat -c",
		"launchctl bootstrap system",
		`launchctl bootout "system/$LAUNCHD_LABEL"`,
		`launchctl print "system/$LAUNCHD_LABEL"`,
		`chmod 700 "$LOG_DIR"`,
		"launchctl bootstrap failed",
		"/Library/Application Support/Pulsix",
		"root:wheel",
		`PULSIX_OS="darwin"`,
		"$BASE_URL/agents/bin/$PULSIX_OS/$PULSIX_ARCH",
		"unsupported system",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered installer must contain %q", want)
		}
	}
}

func TestInstallScriptSyntax(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this system")
	}
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(renderReal(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sh, "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
}
