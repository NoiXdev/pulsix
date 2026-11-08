// Load: the play supervisor and the play uid.
package worker

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestLoadPlaySupervisor: Load, not the environment, names the binary the
// runner re-executes as the play supervisor — this very executable, on
// Linux only. Elsewhere it stays empty and the runner spawns directly.
func TestLoadPlaySupervisor(t *testing.T) {
	cfg, err := Load([]string{"PULSIX_MANAGER_ADDR=manager.example.test:8444"})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" {
		if cfg.PlaySupervisor != "" {
			t.Fatalf("PlaySupervisor = %q on %s, want empty", cfg.PlaySupervisor, runtime.GOOS)
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PlaySupervisor != exe {
		t.Errorf("PlaySupervisor = %q, want os.Executable() = %q", cfg.PlaySupervisor, exe)
	}
	if !filepath.IsAbs(cfg.PlaySupervisor) {
		t.Errorf("PlaySupervisor = %q, want an absolute path", cfg.PlaySupervisor)
	}
}

// TestLoadPlaySupervisorIgnoresEnvironment: there is no variable for it, so
// an operator cannot point the worker at another binary. The value is the
// one Load names without any (this executable on Linux, empty elsewhere),
// not merely something other than the variable's.
func TestLoadPlaySupervisorIgnoresEnvironment(t *testing.T) {
	cfg, err := Load([]string{
		"PULSIX_MANAGER_ADDR=manager.example.test:8444",
		"PULSIX_WORKER_PLAY_SUPERVISOR=/bin/evil",
		"PULSIX_PLAY_SUPERVISOR=/bin/evil",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := ""
	if runtime.GOOS == "linux" {
		if want, err = os.Executable(); err != nil {
			t.Fatal(err)
		}
	}
	if cfg.PlaySupervisor != want {
		t.Fatalf("PlaySupervisor = %q, want %q whatever the environment says", cfg.PlaySupervisor, want)
	}
}

// TestLoadPlayUID (uid separation, Task 1): PULSIX_WORKER_PLAY_UID is
// optional and, when set, must name a uid that is neither root nor the
// worker's own — either would make the de-escalation a no-op or worse.
func TestLoadPlayUID(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  string
		want uint32
		err  string
	}{
		"unset":        {"", 0, ""},
		"set":          {"65533", 65533, ""},
		"not a number": {"nobody", 0, "PULSIX_WORKER_PLAY_UID"},
		"negative":     {"-1", 0, "PULSIX_WORKER_PLAY_UID"},
		"zero is root": {"0", 0, "must not be 0"},
		"own uid":      {strconv.Itoa(os.Getuid()), 0, "the worker's own uid"},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "own uid" && os.Getuid() == 0 {
				// Root's own uid is 0, which "zero is root" refuses
				// first, with its own message (backlog 2.109).
				t.Skip("running as root: the worker's own uid is 0, covered by \"zero is root\"")
			}
			env := []string{
				"PULSIX_MANAGER_ADDR=manager.example.test:8444",
				"PULSIX_WORKER_CERT=/c.pem",
				"PULSIX_WORKER_KEY=/k.pem",
				"PULSIX_CA_CERT=/ca.pem",
			}
			if tc.raw != "" {
				env = append(env, "PULSIX_WORKER_PLAY_UID="+tc.raw)
			}
			cfg, err := Load(env)
			if tc.err == "" {
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if cfg.PlayUID != tc.want {
					t.Errorf("PlayUID = %d, want %d", cfg.PlayUID, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatal("Load must fail")
			}
			if !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("error %q must contain %q", err, tc.err)
			}
		})
	}
}
