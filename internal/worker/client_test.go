// Dial: the mTLS round trip against the real grpcsrv harness — a worker
// certificate this instance CA issued claims a job through it; material
// from a foreign CA authenticates nowhere.
package worker

import (
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/grpcsrv"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// TestDialClaimRoundTrip: a dialed worker client claims a job the harness
// seeded, over the bufconn transport.
func TestDialClaimRoundTrip(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1-a", true)
	jobID := uuid.Must(uuid.NewV7())
	h.claims.Enqueue(&grpcsrv.JobClaim{
		Job:   workerpb.Job{Id: jobID.String(), Kind: "run", OrgId: orgID.String(), ZoneId: zoneID.String()},
		Token: "tok-" + jobID.String(),
	})

	conn, err := h.dialWith(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := workerpb.NewWorkerServiceClient(conn)
	resp, err := client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{
		Version: "dev-test", ZoneIds: []string{zoneID.String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.GetJob().GetId(); got != jobID.String() {
		t.Fatalf("claimed job id %q, want %q", got, jobID.String())
	}
	calls := h.claims.Calls()
	if len(calls) != 1 {
		t.Fatalf("claims = %d, want 1", len(calls))
	}
	if len(calls[0].Scope.ZoneIDs) != 1 || calls[0].Scope.ZoneIDs[0] != zoneID || calls[0].Scope.OrgID != orgID {
		t.Errorf("claimed for scope %+v, want org %s zone %s", calls[0].Scope, orgID, zoneID)
	}
	if calls[0].Version != "dev-test" {
		t.Errorf("version = %q, want the claimed one", calls[0].Version)
	}
}

// TestDialForeignCARejected: a CA pool the server certificate does not
// belong to breaks the handshake at the first RPC.
func TestDialForeignCARejected(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1-a", true)

	// A second, unrelated instance CA; its pool is the only thing the
	// worker pins.
	other := newHarness(t)
	_, otherCert := other.ca.Pool()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: otherCert.Raw})
	cfg.CAPath = writeTempFile(t, cfg.CAPath, caPEM)

	conn, err := h.dialWith(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := workerpb.NewWorkerServiceClient(conn)
	_, err = client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %s (%v), want Unavailable", status.Code(err), err)
	}
}

// TestDialForeignWorkerCertRejected: a client certificate signed by
// another CA authenticates nowhere — the manager refuses it.
func TestDialForeignWorkerCertRejected(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1-a", true)
	other := newHarness(t)
	otherCertPEM, otherKeyPEM, _, err := other.ca.Issue(t.Context(), &orgID, zoneID, "impostor")
	if err != nil {
		t.Fatal(err)
	}
	cfg.CertPath = writeTempFile(t, cfg.CertPath, otherCertPEM)
	cfg.KeyPath = writeTempFile(t, cfg.KeyPath, otherKeyPEM)

	conn, err := h.dialWith(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := workerpb.NewWorkerServiceClient(conn)
	_, err = client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %s (%v), want Unavailable (handshake failure)", status.Code(err), err)
	}
}

// TestDialUnregisteredWorkerRejected: a valid certificate whose serial the
// registry does not know is refused by the manager's auth interceptor.
func TestDialUnregisteredWorkerRejected(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1-a", false) // issued but not registered

	conn, err := h.dialWith(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := workerpb.NewWorkerServiceClient(conn)
	_, err = client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %s (%v), want PermissionDenied", status.Code(err), err)
	}
}

// TestDialBadMaterial: unreadable files fail Dial itself, before any RPC
// can even be attempted.
func TestDialBadMaterial(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1-a", true)

	cases := []struct {
		name    string
		mutate  func(c *Config)
		wantSub string
	}{
		{
			name:    "missing CA file",
			mutate:  func(c *Config) { c.CAPath = filepath.Join(filepath.Dir(c.CAPath), "no-such-ca.pem") },
			wantSub: "read CA pool file",
		},
		{
			name:    "CA file without certificates",
			mutate:  func(c *Config) { c.CAPath = writeTempFile(t, c.CAPath, []byte("not a pem")) },
			wantSub: "contains no PEM certificate",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := cfg
			tc.mutate(&c)
			if _, err := h.dialWith(t, c); err == nil {
				t.Fatal("Dial must fail")
			} else if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q must mention %q", err, tc.wantSub)
			}
		})
	}
}

// TestDialMismatchedKeyRejected: a certificate and key from different
// pairs load fine but never complete a handshake.
func TestDialMismatchedKeyRejected(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1-a", true)
	other := newHarness(t)
	otherCert, otherKey, _, err := other.ca.Issue(t.Context(), &orgID, zoneID, "other")
	if err != nil {
		t.Fatal(err)
	}
	cfg.CertPath = writeTempFile(t, cfg.CertPath, otherCert)
	cfg.KeyPath = writeTempFile(t, cfg.KeyPath, otherKey)

	conn, err := h.dialWith(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := workerpb.NewWorkerServiceClient(conn)
	_, err = client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %s (%v), want Unavailable (handshake failure)", status.Code(err), err)
	}
}

// TestDialServerNameOverride (Task 17): a loopback-style ManagerAddr whose
// host ("127.0.0.1") does not match the server certificate's DNS SAN
// ("manager", the harness's ca.Config.ListenHost) fails the handshake
// exactly as a real bind-all listener would — unless cfg.ServerName names
// the SAN explicitly, in which case the same mismatched address succeeds.
// This pins the fix internal/app's embedded worker (and compose's
// PULSIX_MANAGER_SERVER_NAME) depend on.
func TestDialServerNameOverride(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1-a", true)
	cfg.ManagerAddr = "127.0.0.1:8444" // does not match the "manager" SAN

	t.Run("without override, the mismatched address fails", func(t *testing.T) {
		conn, err := h.dialWith(t, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		client := workerpb.NewWorkerServiceClient(conn)
		_, err = client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"})
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("code = %s (%v), want Unavailable (SAN mismatch)", status.Code(err), err)
		}
	})

	t.Run("with the override naming the SAN, the same address succeeds", func(t *testing.T) {
		withOverride := cfg
		withOverride.ServerName = "manager"
		conn, err := h.dialWith(t, withOverride)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		client := workerpb.NewWorkerServiceClient(conn)
		if _, err := client.ClaimJob(t.Context(), &workerpb.ClaimJobRequest{Version: "dev"}); err != nil {
			t.Fatalf("ClaimJob with ServerName override: %v", err)
		}
	})
}

// writeTempFile overwrites path with data and returns it.
func writeTempFile(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
