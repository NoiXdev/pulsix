package agentdist

import (
	"errors"
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

// Backlog 3.49: install.sh --uninstall [--keep-data] removes what the
// install put down, on Linux and macOS, from fixed paths only.
func TestInstallScriptUninstallShape(t *testing.T) {
	out := renderReal(t)
	for _, want := range []string{
		// The flags and the usage line naming them.
		"--uninstall)",
		"--keep-data)",
		"install.sh --uninstall [--keep-data]",
		// Linux: stop and disable only where systemd runs, then the unit,
		// the binary and (unless --keep-data) /etc/pulsix.
		`systemctl disable --now "$SERVICE_NAME"`,
		`remove_path "$UNIT_PATH"`,
		`remove_path "$BIN_DIR/pulsix-agent"`,
		`remove_path "$DATA_DIR"`,
		`DATA_DIR="/etc/pulsix"`,
		// macOS: bootout with the same wait, then plist, binary, logs, data.
		`remove_path "$PLIST_PATH"`,
		`remove_path "$LOG_DIR"`,
		`LOG_DIR="/Library/Logs/Pulsix"`,
		`DATA_DIR="/Library/Application Support/Pulsix"`,
		"wait_for_launchd_job_gone",
		"Darwin) uninstall_darwin ;;",
		// Idempotent, and the agent stays registered in Pulsix.
		"pulsix-agent: nothing to remove",
		"pulsix-agent: the agent is still registered in Pulsix; revoke or delete it on the Agents page",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered installer must contain %q", want)
		}
	}
	// A re-run upgrades: enable --now leaves a running unit on the old
	// binary (backlog 3.51).
	if strings.Contains(out, "systemctl enable --now") {
		t.Error("the Linux install must enable and restart the unit, not enable --now")
	}
	for _, want := range []string{`systemctl enable "$SERVICE_NAME"`, `systemctl restart "$SERVICE_NAME"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the Linux install must run %q", want)
		}
	}
	// The uninstall runs before anything the install needs (token,
	// systemd, architecture) but after the root check.
	dispatch := strings.Index(out, "Linux) uninstall_linux ;;")
	if dispatch < 0 {
		t.Fatal("the installer must dispatch to the uninstall per system")
	}
	for _, later := range []string{
		`PULSIX_TOKEN="$(read_token_file "$PULSIX_TOKEN_FILE")"`,
		`die "systemd is required`,
		`ARCH="$(uname -m)"`,
	} {
		if i := strings.Index(out, later); i < dispatch {
			t.Errorf("the uninstall must run before %q", later)
		}
	}
	if root := strings.Index(out, `if [ "$(id -u)" != "0" ]; then`); root < 0 || root > dispatch {
		t.Error("the uninstall must run after the root check")
	}
	// The root message fits the mode; the install's stays as it was.
	for _, want := range []string{
		`die "must run as root (e.g. curl ... | sudo sh -s -- --uninstall)"`,
		`die "must run as root (e.g. curl ... | sudo sh -s -- --manager ...)"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the root check must carry %q", want)
		}
	}
	// Linux systemctl steps only where systemd runs.
	if !strings.Contains(out, "[ -d /run/systemd/system ] && [ -f \"$UNIT_PATH\" ]") {
		t.Error("the uninstall must stop the unit only where systemd runs and the unit exists")
	}
	// A unit that is still active after disable --now stops the uninstall
	// before anything is removed, and is never reported as stopped.
	assertInOrder(t, "uninstall_linux", shellFunction(t, out, "uninstall_linux"),
		`systemctl disable --now "$SERVICE_NAME"`,
		`if systemctl is-active --quiet "$SERVICE_NAME"; then`,
		`die "could not stop $SERVICE_NAME`,
		`echo "pulsix-agent: stopped and disabled $SERVICE_NAME"`,
		`remove_path "$UNIT_PATH"`,
	)
	// The macOS uninstall waits for the job to go, like the upgrade.
	assertInOrder(t, "uninstall_darwin", shellFunction(t, out, "uninstall_darwin"),
		`launchctl bootout "system/$LAUNCHD_LABEL"`,
		"wait_for_launchd_job_gone",
		`remove_path "$PLIST_PATH"`,
	)
	// remove_path refuses an empty or relative path before rm -rf sees it.
	assertInOrder(t, "remove_path", shellFunction(t, out, "remove_path"),
		"/?*) ;;",
		`*) die "refusing to remove '$1'" ;;`,
		`rm -rf -- "$1"`,
	)
	// A symlinked data directory is removed as a link, never followed: the
	// only rm -rf calls are remove_path's (fixed paths, no trailing slash)
	// and the download directory's cleanup.
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "rm -rf") || strings.HasPrefix(line, "#") {
			continue
		}
		if line != `rm -rf -- "$1"` && line != `trap 'rm -rf "$TMP"' EXIT` {
			t.Errorf("unexpected rm -rf (only remove_path and the $TMP trap may use it): %q", line)
		}
	}
	for _, path := range []string{"$DATA_DIR", "$LOG_DIR", "$UNIT_PATH", "$PLIST_PATH", "$BIN_DIR/pulsix-agent"} {
		for _, bad := range []string{`remove_path "` + path + `/"`, `remove_path "` + path + `"/`} {
			if strings.Contains(out, bad) {
				t.Errorf("a trailing slash follows a symlink: %q", bad)
			}
		}
	}
}

// shellFunction returns the body of the shell function name: from its
// "name() {" line up to the next line that is a lone "}".
func shellFunction(t *testing.T, script, name string) string {
	t.Helper()
	start := strings.Index(script, "\n"+name+"() {\n")
	if start < 0 {
		t.Fatalf("the installer must define %s()", name)
	}
	body := script[start+1:]
	end := strings.Index(body, "\n}\n")
	if end < 0 {
		t.Fatalf("%s() has no closing brace", name)
	}
	return body[:end]
}

// assertInOrder fails unless every want occurs in text, each after the one before.
func assertInOrder(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	at, prev := 0, "its start"
	for _, want := range wants {
		i := strings.Index(text[at:], want)
		if i < 0 {
			t.Errorf("%s must contain %q after %q", what, want, prev)
			return
		}
		at += i + len(want)
		prev = want
	}
}

// The argument errors exit with the usage (2) before the root check and
// before any file is touched, so they can run here. Accepted invocations
// would reach the root check and are left to the container check.
func TestInstallScriptUninstallUsageErrors(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this system")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: a parser regression would reach the uninstall")
	}
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(renderReal(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--keep-data"},
		{"--manager", "m.example:8444", "--keep-data"},
		{"--uninstall", "--manager", "m.example:8444"},
		{"--uninstall", "--token", "pxa1.a.b.c"},
		{"--uninstall", "--token", ""},
		{"--uninstall", "--token-file", "/nonexistent"},
		{"--uninstall", "--name", "web-1"},
		{"--uninstall", "--server-name", "grpc.example"},
		{"--keep-data", "--uninstall", "--name", "web-1"},
		{"--uninstall", "--bogus"},
		{"--uninstall", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := exec.Command(sh, append([]string{path}, args...)...)
			cmd.Dir = t.TempDir()
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("exit = %v, want the usage exit 2\n%s", err, out)
			}
			if !strings.Contains(string(out), "install.sh --uninstall [--keep-data]") {
				t.Errorf("the usage must name --uninstall [--keep-data]:\n%s", out)
			}
		})
	}
}
