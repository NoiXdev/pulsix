// White-box tests for the identity extraction VerifyClient performs after a
// chain verifies: a certificate signed by this CA's root always comes from
// Issue, which only writes well-formed SANs, so the malformed-SAN branches
// are only reachable for hand-crafted certificates — they are exercised here
// directly instead of through the public API.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"math/big"
	"net/url"
	"testing"
	"time"
)

// selfSigned builds a certificate with arbitrary subject and SANs, signed by
// a throwaway key — identityFrom only reads fields, it never verifies.
func selfSigned(t *testing.T, mutate func(*x509.Certificate)) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	if mutate != nil {
		mutate(tpl)
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	crt, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return crt
}

func uri(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err) // every URI in these tests is well-formed
	}
	return u
}

func TestIdentityFrom(t *testing.T) {
	org, zone := "11111111-1111-7111-8111-111111111111", "22222222-2222-7222-8222-222222222222"
	id, err := identityFrom(selfSigned(t, func(c *x509.Certificate) {
		c.Subject.CommonName = workerCNPrefix + "fra-1"
		c.URIs = []*url.URL{uri(orgURIPrefix + org), uri(zoneURIPrefix + zone)}
	}))
	if err != nil {
		t.Fatalf("identityFrom: %v", err)
	}
	if id.WorkerName != "fra-1" || id.ZoneID.String() != zone || id.OrgID == nil || id.OrgID.String() != org {
		t.Fatalf("identity = %+v", id)
	}
	if id.Kind != KindWorker {
		t.Fatalf("kind = %v, want KindWorker", id.Kind)
	}
	if id.Serial.Sign() <= 0 {
		t.Fatalf("serial = %v", id.Serial)
	}
}

func TestIdentityFromKindDiscrimination(t *testing.T) {
	org, zone := "11111111-1111-7111-8111-111111111111", "22222222-2222-7222-8222-222222222222"
	cases := []struct {
		name     string
		mutate   func(*x509.Certificate)
		kind     Kind
		workerID bool // whether the CN carries the worker prefix
	}{
		{
			"zone SAN present: worker (org-free variant)",
			func(c *x509.Certificate) {
				c.Subject.CommonName = workerCNPrefix + "fra-1"
				c.URIs = []*url.URL{uri(zoneURIPrefix + zone)}
			},
			KindWorker, true,
		},
		{
			"zone and org SANs: worker",
			func(c *x509.Certificate) {
				c.Subject.CommonName = workerCNPrefix + "fra-1"
				c.URIs = []*url.URL{uri(orgURIPrefix + org), uri(zoneURIPrefix + zone)}
			},
			KindWorker, true,
		},
		{
			"org SAN only: agent",
			func(c *x509.Certificate) {
				c.Subject.CommonName = agentCNPrefix + "agt-1"
				c.URIs = []*url.URL{uri(orgURIPrefix + org)}
			},
			KindAgent, true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, err := identityFrom(selfSigned(t, c.mutate))
			if err != nil {
				t.Fatalf("identityFrom: %v", err)
			}
			if id.Kind != c.kind {
				t.Fatalf("kind = %v, want %v", id.Kind, c.kind)
			}
			if c.workerID != (id.WorkerName != "") {
				t.Fatalf("worker name = %q", id.WorkerName)
			}
		})
	}
}

func TestIdentityFromRejectsBadCertificates(t *testing.T) {
	org, zone := "11111111-1111-7111-8111-111111111111", "22222222-2222-7222-8222-222222222222"
	cases := []struct {
		name   string
		mutate func(*x509.Certificate)
	}{
		{
			"no zone SAN (neither prefix)",
			func(c *x509.Certificate) {
				c.Subject.CommonName = workerCNPrefix + "fra-1"
				c.URIs = []*url.URL{uri("urn:other:thing")}
			},
		},
		{
			"org SAN only with worker CN (mixed shape)",
			func(c *x509.Certificate) {
				c.Subject.CommonName = workerCNPrefix + "fra-1"
				c.URIs = []*url.URL{uri(orgURIPrefix + org)}
			},
		},
		{
			"no SANs at all",
			func(c *x509.Certificate) {
				c.Subject.CommonName = agentCNPrefix + "agt-1"
			},
		},
		{
			"malformed zone uuid",
			func(c *x509.Certificate) {
				c.Subject.CommonName = workerCNPrefix + "fra-1"
				c.URIs = []*url.URL{uri(zoneURIPrefix + "not-a-uuid")}
			},
		},
		{
			"malformed org uuid (agent shape)",
			func(c *x509.Certificate) {
				c.Subject.CommonName = agentCNPrefix + "agt-1"
				c.URIs = []*url.URL{uri(orgURIPrefix + "not-a-uuid")}
			},
		},
		{
			"common name is not a worker",
			func(c *x509.Certificate) {
				c.Subject.CommonName = "someone-else"
				c.URIs = []*url.URL{uri(zoneURIPrefix + zone)}
			},
		},
		{
			"empty worker name",
			func(c *x509.Certificate) {
				c.Subject.CommonName = workerCNPrefix
				c.URIs = []*url.URL{uri(zoneURIPrefix + zone)}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := identityFrom(selfSigned(t, c.mutate))
			if !errors.Is(err, ErrBadIdentity) {
				t.Fatalf("identityFrom = %v, want ErrBadIdentity", err)
			}
		})
	}
}
