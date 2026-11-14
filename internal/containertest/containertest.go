// Package containertest gates the module's container-based integration
// tests (the check plugins' real servers, the snmp probe's real daemon) on
// a Docker daemon that runs Linux containers. The agent's unit tests also
// run on macOS and Windows, where CI has no such daemon (macOS runners
// have no Docker at all, Windows runners run Windows containers), so there
// the tests skip; on the Linux go job, which sets RequiredEnv, a missing
// daemon fails them instead. It is imported from _test.go files only.
package containertest

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

// RequiredEnv, set to "1", turns a missing Linux-container Docker from a
// skip into a failure, so CI on Linux cannot go green by skipping.
const RequiredEnv = "PULSIX_TEST_REQUIRE_DOCKER"

// probeTimeout bounds the daemon query; testcontainers' own connection
// attempts are bounded by it as well.
const probeTimeout = 30 * time.Second

var (
	once   sync.Once
	reason string // why no Linux-container Docker is usable; "" when it is
)

// RequireLinuxDocker returns when a Docker daemon running Linux containers
// answers. Otherwise it skips t, or fails it when RequiredEnv is set. The
// daemon is probed once per test binary.
func RequireLinuxDocker(t testing.TB) {
	t.Helper()
	once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		reason = unusable(probe(ctx))
	})
	gate(t, required(), reason)
}

// tb is the part of testing.TB gate uses, so its test can watch it.
type tb interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

func gate(t tb, required bool, reason string) {
	t.Helper()
	if reason == "" {
		return
	}
	if required {
		t.Fatalf("%s=1 but %s", RequiredEnv, reason)
		return
	}
	t.Skipf("needs a Docker daemon running Linux containers: %s (set %s=1 to fail instead)", reason, RequiredEnv)
}

func required() bool { return os.Getenv(RequiredEnv) == "1" }

// unusable says why a daemon whose probe returned osType and err cannot run
// the tests' Linux images, or "" when it can.
func unusable(osType string, err error) string {
	if err != nil {
		return fmt.Sprintf("no Docker provider: %v", err)
	}
	if osType != "linux" {
		return fmt.Sprintf("the Docker daemon runs %q containers, not Linux ones", osType)
	}
	return ""
}

// probe asks the provider testcontainers would use which OS its daemon runs
// containers for. testcontainers panics on some broken setups (an
// unresolvable socket), which is a missing provider here too.
func probe(ctx context.Context) (osType string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("docker provider panicked: %v", r)
		}
	}()
	p, err := testcontainers.ProviderDefault.GetProvider()
	if err != nil {
		return "", fmt.Errorf("get provider: %w", err)
	}
	defer func() { _ = p.Close() }()
	dp, ok := p.(*testcontainers.DockerProvider)
	if !ok {
		return "", fmt.Errorf("unexpected provider %T", p)
	}
	info, err := dp.Client().Info(ctx, client.InfoOptions{})
	if err != nil {
		return "", fmt.Errorf("docker info: %w", err)
	}
	return info.Info.OSType, nil
}
