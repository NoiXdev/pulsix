// Enroll: the anonymous token redemption over the wire — both host flows,
// the name resolution order, every refusal shape, the concurrent last-use
// race and the per-address rate limit.
package agentapi

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/enrolltoken"
)

var testOrg = uuid.MustParse("11111111-1111-7111-8111-111111111111")

func tokenInput(hint string) agents.TokenInput {
	return agents.TokenInput{ZoneID: uuid.Must(uuid.NewV7()), NameHint: hint, MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)}
}

func mustCert(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	crt, err := ca.ParseLeaf([]byte(certPEM))
	if err != nil {
		t.Fatalf("parse enrolled certificate: %v", err)
	}
	return crt
}

// TestEnrollCreatesHostFromToken: an unbound token's first claim creates
// the host, named by the token's hint (which also names the agent and its
// certificate); the response carries the certificate for the agent's own
// key, the CA PEM and the host id.
func TestEnrollCreatesHostFromToken(t *testing.T) {
	h := newHarness(t)
	tok := h.enrollToken(testOrg, tokenInput("from-hint"))
	_, csr := newKeyAndCSR(t)
	resp, err := h.enroll(tok.raw, csr, "presented")
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetHostId() == "" {
		t.Fatal("enroll response carries no host id")
	}
	crt := mustCert(t, resp.GetCertPem())
	if crt.Subject.CommonName != "pulsix-agent from-hint" {
		t.Fatalf("cn = %q, want the hint-named agent", crt.Subject.CommonName)
	}
	if _, err := h.ca.VerifyClient([][]byte{crt.Raw}); err != nil {
		t.Fatalf("the enrolled certificate does not verify: %v", err)
	}
	if resp.GetCaPem() != string(h.ca.CAPEM()) {
		t.Fatal("the response's CA PEM must be the instance root")
	}
	if got := h.prov.monitoredOf(uuid.MustParse(resp.GetHostId())); !got {
		t.Fatal("enrollment did not flip the created host's monitored flag")
	}
	// The registry row and the audit trail.
	row, err := h.agentSvc.GetBySerial(t.Context(), crt.SerialNumber.String())
	if err != nil || row.OrgID != testOrg || row.Name != "from-hint" {
		t.Fatalf("registry row = %+v, %v", row, err)
	}
	// The certificate row records the agent's key, which adoption after a
	// restore compares against (backlog 7.47).
	if got := h.store.registeredKey(crt.SerialNumber.String()); got != agents.KeyFingerprint(crt) {
		t.Fatalf("registered key = %q, want the leaf's %q", got, agents.KeyFingerprint(crt))
	}
	events := h.audit.recorded()
	if len(events) != 1 || events[0].Operation != "agent.enroll" || events[0].OrgID != testOrg.String() {
		t.Fatalf("audit = %+v", events)
	}
	if strings.Contains(events[0].Summary, tok.raw) {
		t.Fatal("token material must never land in the audit record")
	}
}

// TestEnrollNameResolutionOrder pins the create flow's name order at the
// gRPC level: hint → presented name; no hint and no name is refused.
func TestEnrollNameResolutionOrder(t *testing.T) {
	h := newHarness(t)

	// Hint wins over the presented name.
	tok := h.enrollToken(testOrg, tokenInput("hint-name"))
	_, csr := newKeyAndCSR(t)
	resp, err := h.enroll(tok.raw, csr, "presented-name")
	if err != nil || mustCert(t, resp.GetCertPem()).Subject.CommonName != "pulsix-agent hint-name" {
		t.Fatalf("hinted enroll = %v, %v", resp, err)
	}

	// Without a hint the presented name stands.
	blank := h.enrollToken(testOrg, tokenInput(""))
	_, csr2 := newKeyAndCSR(t)
	resp, err = h.enroll(blank.raw, csr2, "presented-name")
	if err != nil || mustCert(t, resp.GetCertPem()).Subject.CommonName != "pulsix-agent presented-name" {
		t.Fatalf("blank-hint enroll = %v, %v", resp, err)
	}

	// Neither: refused, and the use is not consumed (the claim rolls back).
	neither := h.enrollToken(testOrg, tokenInput(""))
	if _, err := h.enroll(neither.raw, csr2, ""); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("no name anywhere: %v, want InvalidArgument", err)
	}
	if uses, used := h.store.useOf(neither.parsed.ID); uses != 0 || used {
		t.Fatalf("the refused claim consumed a use: uses=%d used=%v", uses, used)
	}
	_, csr3 := newKeyAndCSR(t)
	if _, err := h.enroll(neither.raw, csr3, "retry-name"); err != nil {
		t.Fatalf("the refused claim must not consume its use: %v", err)
	}
}

// TestEnrollBindsToPreCreatedHost: a token bound to an existing host
// claims exactly that host; the agent's presented name names the
// certificate (the host keeps its own).
func TestEnrollBindsToPreCreatedHost(t *testing.T) {
	h := newHarness(t)
	bound := h.prov.seedHost(testOrg, "web-1")
	in := tokenInput("")
	in.HostID = &bound
	tok := h.enrollToken(testOrg, in)
	_, csr := newKeyAndCSR(t)
	resp, err := h.enroll(tok.raw, csr, "agent-of-web-1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetHostId() != bound.String() {
		t.Fatalf("host id = %s, want the bound %s", resp.GetHostId(), bound)
	}
	if mustCert(t, resp.GetCertPem()).Subject.CommonName != "pulsix-agent agent-of-web-1" {
		t.Fatal("the presented name must name the bound flow's certificate")
	}
	if !h.prov.monitoredOf(bound) {
		t.Fatal("the bound host was not flipped monitored")
	}
}

// TestEnrollRefusesInvalidTokens: unknown, wrong-secret, expired,
// exhausted and malformed tokens are all the same Unauthenticated.
func TestEnrollRefusesInvalidTokens(t *testing.T) {
	h := newHarness(t)
	_, csr := newKeyAndCSR(t)

	unknownID := uuid.Must(uuid.NewV7())
	unknownSecret := make([]byte, 32)
	unknown := enrolltokenWire{raw: enrolltoken.AgentString(enrolltoken.Token{ID: unknownID, Secret: unknownSecret}),
		parsed: enrolltoken.Token{ID: unknownID, Secret: unknownSecret}}
	stolen := unknown.parsed
	stolen.Secret[0] ^= 0xff

	// An expired token cannot come through CreateToken (it refuses past
	// expiries), so the store is seeded directly.
	expiredID := uuid.Must(uuid.NewV7())
	expiredSecret := make([]byte, 32)
	sum := sha256.Sum256(expiredSecret)
	if _, err := h.store.CreateToken(context.Background(), agents.Token{
		ID: expiredID, OrgID: testOrg, ZoneID: uuid.Must(uuid.NewV7()), MaxUses: 1,
		ExpiresAt: time.Now().Add(-time.Minute), CreatedAt: time.Now(),
	}, sum[:]); err != nil {
		t.Fatal(err)
	}

	exhausted := h.enrollToken(testOrg, tokenInput("n"))
	if _, err := h.agentSvc.ClaimToken(context.Background(), testOrg, exhausted.parsed, "pre-claim"); err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{
		"unknown":                        unknown.raw,
		"wrong secret":                   enrolltoken.AgentString(stolen),
		"expired":                        enrolltoken.AgentString(enrolltoken.Token{ID: expiredID, Secret: expiredSecret}),
		"exhausted":                      exhausted.raw,
		"malformed":                      "not-a-token",
		"worker token in the agent path": workerTokenString,
	} {
		if _, err := h.enroll(token, csr, "x"); status.Code(err) != codes.Unauthenticated {
			t.Errorf("%s: %v, want Unauthenticated", name, err)
		}
	}
}

// workerTokenString is a pxw1 token: a foreign prefix is malformed in the
// agent path.
var workerTokenString = "pxw1.AAAAAAAAAAAAAAAAAAAAAA.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// TestEnrollConcurrentSingleUseRace: two concurrent enrolls of a 1-use
// token — exactly one wins, the loser is refused, and only one host and
// one agent exist afterwards.
func TestEnrollConcurrentSingleUseRace(t *testing.T) {
	h := newHarness(t)
	tok := h.enrollToken(testOrg, tokenInput("race"))
	_, csr1 := newKeyAndCSR(t)
	_, csr2 := newKeyAndCSR(t)

	const contenders = 2
	type outcome struct {
		err error
	}
	results := make(chan outcome, contenders)
	for _, csr := range []string{csr1, csr2} {
		go func(csr string) {
			_, err := h.enroll(tok.raw, csr, "race")
			results <- outcome{err}
		}(csr)
	}
	wins := 0
	for i := 0; i < contenders; i++ {
		out := <-results
		if out.err == nil {
			wins++
			continue
		}
		if status.Code(out.err) != codes.Unauthenticated {
			t.Fatalf("loser: %v, want Unauthenticated", out.err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d concurrent enrolls won, want exactly one", wins)
	}
	if got := len(h.prov.hosts[testOrg]); got != 1 {
		t.Fatalf("%d hosts created, want one", got)
	}
	if got := len(h.audit.recorded()); got != 1 {
		t.Fatalf("%d audit events, want one", got)
	}
}

// TestEnrollIsRateLimitedPerAddress: ten attempts per minute from one
// address, the eleventh refused, the window reset by the clock.
func TestEnrollIsRateLimitedPerAddress(t *testing.T) {
	h := newHarness(t)
	_, csr := newKeyAndCSR(t)
	for i := 0; i < agentEnrollLimit; i++ {
		if _, err := h.enroll("not-a-token", csr, "x"); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("attempt %d: %v, want Unauthenticated", i, err)
		}
	}
	if _, err := h.enroll("not-a-token", csr, "x"); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("over the limit: %v, want ResourceExhausted", err)
	}
	h.advance(agentEnrollWindow + time.Second)
	if _, err := h.enroll("not-a-token", csr, "x"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("after the window: %v, want Unauthenticated", err)
	}
}

// TestEnrollBadCSRDoesNotConsume: a rejected certificate request costs no
// token use — the agent can retry with a fixed CSR.
func TestEnrollBadCSRDoesNotConsume(t *testing.T) {
	h := newHarness(t)
	tok := h.enrollToken(testOrg, tokenInput("web-1"))
	if _, err := h.enroll(tok.raw, "garbage", "x"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad csr: %v, want InvalidArgument", err)
	}
	_, csr := newKeyAndCSR(t)
	if _, err := h.enroll(tok.raw, csr, "x"); err != nil {
		t.Fatalf("retry after a bad csr: %v", err)
	}
}

// TestEnrollNameConflict: a created host's name taken mid-flight is
// AlreadyExists, not a generic failure.
func TestEnrollNameConflict(t *testing.T) {
	h := newHarness(t)
	h.prov.seedHost(testOrg, "taken")
	tok := h.enrollToken(testOrg, tokenInput("taken"))
	_, csr := newKeyAndCSR(t)
	if _, err := h.enroll(tok.raw, csr, "x"); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("name clash: %v, want AlreadyExists", err)
	}
}

// TestEnrollVanishedBoundHost: a token bound to a host that no longer
// exists reports NotFound and consumes nothing.
func TestEnrollVanishedBoundHost(t *testing.T) {
	h := newHarness(t)
	missing := uuid.Must(uuid.NewV7())
	in := tokenInput("")
	in.HostID = &missing
	tok := h.enrollToken(testOrg, in)
	_, csr := newKeyAndCSR(t)
	if _, err := h.enroll(tok.raw, csr, "x"); status.Code(err) != codes.NotFound {
		t.Fatalf("vanished bound host: %v, want NotFound", err)
	}
	if uses, used := h.store.useOf(tok.parsed.ID); uses != 0 || used {
		t.Fatalf("the failed claim consumed a use: uses=%d used=%v", uses, used)
	}
}

// TestNewRequiresWiring guards the constructor contract.
func TestNewRequiresWiring(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("New without CA/Agents must fail")
	}
	if _, err := New(Deps{CA: &ca.CA{}, Agents: &agents.Service{}}); err == nil {
		t.Fatal("New without Audit must fail")
	}
}

// TestEnrollWithoutAClientCertificate is the headline property: the
// anonymous connection reaches Enroll (and only Enroll — the method-wide
// check lives in grpcsrv's auth tests); here the real service answers.
func TestEnrollWithoutAClientCertificate(t *testing.T) {
	h := newHarness(t)
	tok := h.enrollToken(testOrg, tokenInput("web-9"))
	_, csr := newKeyAndCSR(t)
	resp, err := h.enroll(tok.raw, csr, "web-9")
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetHostId() == "" || resp.GetCertPem() == "" {
		t.Fatalf("enroll = %+v", resp)
	}
}

// TestEnrollWithAClientCertificate: Enroll skips authentication by method,
// so a caller that presents a certificate (an agent re-enrolling with a
// new token) is served like an anonymous one — the token is the credential.
func TestEnrollWithAClientCertificate(t *testing.T) {
	h := newHarness(t)
	first := h.enrollToken(testOrg, tokenInput("web-1"))
	_, csr := newKeyAndCSR(t)
	resp, err := h.enroll(first.raw, csr, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	crt := mustCert(t, resp.GetCertPem())
	hostID := uuid.MustParse(resp.GetHostId())
	in := tokenInput("")
	in.HostID = &hostID
	second := h.enrollToken(testOrg, in)
	if _, err := h.enroll(second.raw, csr, "web-1"); err != nil {
		t.Fatalf("re-enroll with a client certificate: %v, want the token to be the credential", err)
	}
	if row, err := h.agentSvc.GetBySerial(context.Background(), crt.SerialNumber.String()); err != nil || !row.Revoked {
		t.Fatalf("re-enrollment must revoke the replaced serial: %+v, %v", row, err)
	}
}
