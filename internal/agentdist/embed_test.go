// The agent distribution's consistency: the embedded install script and
// systemd unit are byte-identical with their canonical sources under
// deploy/, the template parses and renders, and the dist is only "ready"
// when real binaries are embedded.
package agentdist

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"text/template"
)

// The relative paths from this package's directory to the canonical
// sources (the test binary's cwd is the package dir).
const (
	deployInstallScript = "../../deploy/install.sh"
	deployUnit          = "../../deploy/agent/systemd/pulsix-agent.service"
)

func TestEmbeddedFilesMatchDeploySources(t *testing.T) {
	for _, tt := range []struct {
		name string
		path string
		want []byte
	}{
		{"install.sh", deployInstallScript, InstallScript()},
		{"pulsix-agent.service", deployUnit, Unit()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			canon, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read %s: %v", tt.path, err)
			}
			if string(tt.want) != string(canon) {
				t.Fatalf("the embedded %s drifted from its source under deploy/ — copy the canonical file into internal/agentdist/files/",
					tt.path)
			}
		})
	}
}

func TestInstallScriptTemplate(t *testing.T) {
	script := string(InstallScript())
	for _, want := range []string{"{{.BaseURL}}", "{{.Unit}}"} {
		if !strings.Contains(script, want) {
			t.Errorf("the template must carry %s", want)
		}
	}
	tmpl, err := template.New("install.sh").Parse(script)
	if err != nil {
		t.Fatalf("parse install.sh template: %v", err)
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, map[string]any{"BaseURL": "https://manager.example", "Unit": "unit-text-marker"}); err != nil {
		t.Fatalf("execute install.sh template: %v", err)
	}
	rendered := sb.String()
	for _, bad := range []string{"{{", "}}"} {
		if strings.Contains(rendered, bad) {
			t.Errorf("the rendered script must not carry template syntax:\n%s", rendered)
		}
	}
	for _, want := range []string{
		`BASE_URL="https://manager.example"`,
		"$BASE_URL/agents/bin/linux/$PULSIX_ARCH",
		"$BASE_URL/agents/bin/linux/$PULSIX_ARCH.sha256",
		`BIN_DIR="/usr/local/bin"`,
		"$BIN_DIR/pulsix-agent",
		"/etc/pulsix/agent.env",
		"PULSIX_ENROLL_TOKEN=$PULSIX_TOKEN",
		"PULSIX_MANAGER_ADDR=$PULSIX_MANAGER",
		// The manager's gRPC certificate carries the DNS SAN its own
		// PULSIX_GRPC_SERVER_NAME sets, which is not always the address a
		// host dials — the worker's enrollment snippet passes the
		// override for exactly that reason, so the installer can too.
		"PULSIX_MANAGER_SERVER_NAME=$PULSIX_SERVER_NAME",
		"--server-name",
		"unit-text-marker",
		"systemctl enable --now",
		"sha256sum",
		"--token", "--manager", "--name",
		// Backlog 7.30: the token can come from the terminal or a
		// root-only file instead of the command line.
		"--token-file", "/dev/tty",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the rendered install script must contain %q", want)
		}
	}
}

func TestUnitContent(t *testing.T) {
	unit := string(Unit())
	for _, want := range []string{
		"EnvironmentFile=/etc/pulsix/agent.env",
		"ExecStart=/usr/local/bin/pulsix-agent",
		"Restart=on-failure",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("the systemd unit must contain %q:\n%s", want, unit)
		}
	}
}

// Files() reports the dist this build actually carries, and both answers
// are a contract: nil in a plain dev build (the placeholder only, which is
// what turns the download route into its documented 503), non-nil with
// servable binaries once `make agent-binaries` has run — which is exactly
// what a release build does before compiling the manager, so the test
// cannot assume either state.
func TestFilesReportsWhatThisBuildCarries(t *testing.T) {
	dist := Files()
	built, err := embedded.ReadDir(distRoot)
	if err != nil {
		t.Fatal(err)
	}
	var binaries []string
	for _, e := range built {
		if !e.IsDir() && strings.HasPrefix(e.Name(), BinaryPrefix) && !strings.HasSuffix(e.Name(), ChecksumSuffix) {
			binaries = append(binaries, e.Name())
		}
	}
	if len(binaries) == 0 {
		if dist != nil {
			t.Fatal("Files() in a plain dev build = non-nil, want nil (placeholder only)")
		}
		return
	}
	if dist == nil {
		t.Fatalf("Files() = nil although the dist carries %v", binaries)
	}
	for _, name := range binaries {
		// What the route does: open the name by itself, rooted at the
		// serving root rather than at the embed path.
		b, err := fs.ReadFile(dist, name)
		if err != nil {
			t.Errorf("read %s through Files(): %v", name, err)
			continue
		}
		if len(b) == 0 {
			t.Errorf("%s is empty", name)
		}
		if _, err := fs.ReadFile(dist, name+ChecksumSuffix); err != nil {
			t.Errorf("%s has no checksum beside it: %v", name, err)
		}
	}
}

// The build step that fills the dist is the other half of this package's
// contract: `make agent-binaries` must write exactly the names Files()
// recognizes and the routes look up, into exactly the directory embedded
// here. Nothing else in the repository would notice if it drifted — a
// manager would simply build without binaries and answer 503 forever.
func TestAgentBinariesScriptFillsThisDist(t *testing.T) {
	const script = "../../scripts/agent-binaries.sh"
	info, err := os.Stat(script)
	if err != nil {
		t.Fatalf("stat %s: %v", script, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("%s is not executable (mode %v)", script, info.Mode().Perm())
	}
	b, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		// Where it writes: the directory this package embeds.
		"internal/agentdist/" + distRoot,
		// What it names: the prefix Files() recognizes a binary by and
		// the checksum suffix install.sh fetches.
		BinaryPrefix,
		ChecksumSuffix,
		// The targets install.sh's uname mapping can ask for.
		"linux/amd64",
		"linux/arm64",
		// The agent's own build stamp (cmd/agent/main.go's version var).
		"main.version=",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("%s must reference %q — the dist contract lives in this package", script, want)
		}
	}
	// It clears the dist before building, so it has to know about the one
	// file that must survive that: without the placeholder, a tree that
	// never ran the script again would not compile.
	if !strings.Contains(src, placeholderName) {
		t.Errorf("%s must name %s — it clears the dist, and that file must survive", script, placeholderName)
	}
}

// distFS is what Files() is: the dist's servable view, or nil when the
// dist carries no binary. Files() itself can only ever exercise the state
// THIS build is in, so the other state is pinned here against an
// in-memory dist of the same shape.
func TestDistFSReportsBinariesOnly(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files fstest.MapFS
		want  bool // want a servable filesystem back
	}{
		{"placeholder only", fstest.MapFS{
			distRoot + "/" + placeholderName: {Data: []byte("build artifact placeholder")},
		}, false},
		{"checksum without its binary", fstest.MapFS{
			distRoot + "/" + placeholderName:                               {Data: []byte("placeholder")},
			distRoot + "/" + BinaryPrefix + "linux-amd64" + ChecksumSuffix: {Data: []byte("abc  x")},
		}, false},
		{"a real binary", fstest.MapFS{
			distRoot + "/" + placeholderName:                     {Data: []byte("placeholder")},
			distRoot + "/" + BinaryPrefix + "linux-amd64":        {Data: []byte("ELF")},
			distRoot + "/" + BinaryPrefix + "linux-amd64.sha256": {Data: []byte("abc  x")},
		}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := distFS(tt.files)
			if (got != nil) != tt.want {
				t.Fatalf("distFS() non-nil = %v, want %v", got != nil, tt.want)
			}
			if !tt.want {
				return
			}
			// Rooted at the serving root: the route opens the bare name.
			if _, err := fs.ReadFile(got, BinaryPrefix+"linux-amd64"); err != nil {
				t.Errorf("read the binary through distFS: %v", err)
			}
		})
	}
}

// A dist directory that is not there at all is a build wiring bug, not a
// runtime state: distFS says so loudly instead of serving nothing.
func TestDistFSPanicsWithoutTheDistDirectory(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("distFS on a filesystem without the dist directory did not panic")
		}
	}()
	distFS(fstest.MapFS{"files/install.sh": {Data: []byte("#!/bin/sh\n")}})
}

// Same for the two embedded files: their absence cannot happen at
// runtime (the embed carries them), so it is reported as the build bug it
// would be rather than as an empty script.
func TestMustReadPanicsOnAMissingFile(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("mustRead of a file the embed does not carry did not panic")
		}
	}()
	mustRead("files/there-is-no-such-file")
}
