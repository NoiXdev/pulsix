package docker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// smokeSocket is the local engine's socket for TestDockerSmokeAgainstLocalEngine:
// PULSIX_DOCKER_SOCKET, else the first of the usual paths that exists.
func smokeSocket() string {
	if s := os.Getenv("PULSIX_DOCKER_SOCKET"); s != "" {
		return s
	}
	candidates := []string{"/var/run/docker.sock"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".docker", "run", "docker.sock"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// TestDockerSmokeAgainstLocalEngine runs the docker check with resources on
// against the engine on this machine, read-only: the plugin only lists,
// inspects and reads one-shot stats; it starts nothing and changes
// nothing. Skipped unless PULSIX_DOCKER_SMOKE=1 and a socket exists, so it
// never runs in CI or make test:
//
//	PULSIX_DOCKER_SMOKE=1 go test -run DockerSmoke -v ./internal/agent/checks/plugins/docker/
func TestDockerSmokeAgainstLocalEngine(t *testing.T) {
	if os.Getenv("PULSIX_DOCKER_SMOKE") != "1" {
		t.Skip("set PULSIX_DOCKER_SMOKE=1 to run against the local engine")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket only")
	}
	sock := smokeSocket()
	if sock == "" {
		t.Skip("no engine socket found; set PULSIX_DOCKER_SOCKET")
	}
	p := New().(checks.StatefulPlugin).NewInstance()
	params := map[string]any{"resources": true, "socket": sock}
	ctx := context.Background()
	if _, err := p.Collect(ctx, params); err != nil {
		t.Logf("first round advisory: %v", err)
	}
	time.Sleep(2 * time.Second) // a real interval for the rates
	obs, err := p.Collect(ctx, params)
	if err != nil {
		t.Logf("second round advisory: %v", err)
	}
	running := map[string]bool{}
	byItem := map[string]map[string]float64{}
	for _, o := range obs {
		if o.Err != "" {
			t.Errorf("%s %s: %s", o.Item, o.Metric, o.Err)
			continue
		}
		if o.Metric == "docker.state" && o.Label != nil && *o.Label == "running" {
			running[o.Item] = true
		}
		if o.Value != nil {
			if byItem[o.Item] == nil {
				byItem[o.Item] = map[string]float64{}
			}
			byItem[o.Item][o.Metric] = *o.Value
		}
	}
	t.Logf("engine %s: %v containers, %v running, %v unsampled",
		sock, byItem[""]["docker.containers_total"], byItem[""]["docker.containers_running"], byItem[""]["docker.containers_unsampled"])
	if len(running) == 0 {
		t.Skip("no running container to check")
	}
	maxCPU := float64(runtime.NumCPU())*100 + 50 // the engine may see a VM with other CPUs; generous
	checked := 0
	for name := range running {
		v := byItem[name]
		if v["docker.memory_bytes"] == 0 && v["docker.cpu_percent"] == 0 {
			// Listed running, read stopped: it stopped between the list and
			// its stats, which reads zero by design. Other tools on a dev
			// machine start and stop containers all the time.
			t.Logf("%s: stopped during the round, not judged", name)
			continue
		}
		checked++
		t.Logf("%s: cpu %.2f%% mem %.0f B (%.2f%%) net rx %.0f B/s tx %.0f B/s block r %.0f B/s w %.0f B/s",
			name, v["docker.cpu_percent"], v["docker.memory_bytes"], v["docker.memory_percent"],
			v["docker.net_rx_bytes_per_s"], v["docker.net_tx_bytes_per_s"],
			v["docker.block_read_bytes_per_s"], v["docker.block_write_bytes_per_s"])
		cpu, ok := v["docker.cpu_percent"]
		if !ok || cpu < 0 || cpu > maxCPU {
			t.Errorf("%s: cpu %v (reported %v), want 0..%v", name, cpu, ok, maxCPU)
		}
		if mem := v["docker.memory_bytes"]; mem <= 0 {
			t.Errorf("%s: memory %v, want > 0", name, mem)
		}
		if pct, ok := v["docker.memory_percent"]; !ok || pct <= 0 || pct > 100 {
			t.Errorf("%s: memory percent %v (reported %v), want 0..100", name, pct, ok)
		}
		for _, m := range []string{"docker.net_rx_bytes_per_s", "docker.net_tx_bytes_per_s", "docker.block_read_bytes_per_s", "docker.block_write_bytes_per_s"} {
			if r, ok := v[m]; ok && r < 0 {
				t.Errorf("%s: %s %v", name, m, r)
			}
		}
		if _, ok := v["docker.block_read_bytes_per_s"]; !ok {
			t.Errorf("%s: no block read rate", name)
		}
	}
	if checked == 0 {
		t.Error("no running container could be judged")
	}
}
