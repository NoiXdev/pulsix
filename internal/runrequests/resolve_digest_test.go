package runrequests_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
)

var testVarsKey = runrequests.VarsDigestKey([32]byte{9, 9, 9})

func TestVarsDigestKey(t *testing.T) {
	master := [32]byte{1}
	k := runrequests.VarsDigestKey(master)
	if len(k) != 32 || !bytes.Equal(k, runrequests.VarsDigestKey(master)) {
		t.Fatalf("key = %x, want 32 stable bytes", k)
	}
	if bytes.Equal(k, master[:]) || bytes.Equal(k, runrequests.VarsDigestKey([32]byte{2})) {
		t.Fatal("the key must be derived from, and differ with, the master key")
	}
}

// TestFingerprintCoversIDs: swapping something the run uses for another
// one of the same name shows the same names, so only the ids can tell the
// person something changed.
func TestFingerprintCoversIDs(t *testing.T) {
	cases := map[string]func(w *world){
		"template credential of the same name": func(w *world) {
			other := id()
			w.creds[other] = credentials.Credential{Name: "host-login"}
			tmpl := w.tmpls.fakeTemplates[w.x]
			tmpl.CredentialID = &other
			w.tmpls.fakeTemplates[w.x] = tmpl
		},
		"host credential of the same name": func(w *world) {
			other := id()
			w.creds[other] = credentials.Credential{Name: "host-login"}
			s := w.ssh.ssh[w.h1]
			s.CredentialID = &other
			w.ssh.ssh[w.h1] = s
		},
		"jump host of the same name": func(w *world) {
			other := id()
			w.ssh.bastions[other] = sshconf.Bastion{Name: "jump-1"}
			s := w.ssh.ssh[w.h1]
			s.BastionID = &other
			w.ssh.ssh[w.h1] = s
		},
		"repo git credential of the same name": func(w *world) {
			other := id()
			w.creds[other] = credentials.Credential{Name: "git-deploy"}
			r := w.repos[w.repo]
			r.CredentialID = &other
			w.repos[w.repo] = r
		},
		"repo git credential removed": func(w *world) {
			r := w.repos[w.repo]
			r.CredentialID = nil
			w.repos[w.repo] = r
		},
		"vault credential of the same name": func(w *world) {
			other := id()
			w.creds[other] = credentials.Credential{Name: "prod-vault"}
			tmpl := w.tmpls.fakeTemplates[w.x]
			tmpl.VaultCredentials = []templates.VaultCredential{{CredentialID: other, Label: "prod"}}
			w.tmpls.fakeTemplates[w.x] = tmpl
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x}
			before := runrequests.Fingerprint(req.ID, w.picture(t, req))
			mutate(w)
			if after := runrequests.Fingerprint(req.ID, w.picture(t, req)); after == before {
				t.Fatalf("%s left the fingerprint unchanged", name)
			}
		})
	}
}

// TestVarsDigestCoversSecretValues: the digest follows the real values of
// group, membership and extra vars, secrets included, while neither the
// picture nor its fingerprint carries a secret.
func TestVarsDigestCoversSecretValues(t *testing.T) {
	const groupSecret, memberSecret, extraSecret = "group-s3cret-4f1a", "member-s3cret-77b2", "extra-s3cret-c09e"
	setVars := func(w *world, group, member, extra string) {
		w.inv.doc = map[string]any{
			"web":   map[string]any{"hosts": []any{"web-1"}, "vars": map[string]any{"db_pass": group}},
			"_meta": map[string]any{"hostvars": map[string]any{"web-1": map[string]any{"host_pass": member}}},
		}
		w.inv.extra = map[string]any{"tag": "v1", "api_key": extra}
	}
	w := newWorld()
	setVars(w, groupSecret, memberSecret, extraSecret)
	req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x}
	p := w.picture(t, req)
	doc, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	fp := runrequests.Fingerprint(req.ID, p)
	for _, secret := range []string{groupSecret, memberSecret, extraSecret} {
		if strings.Contains(string(doc), secret) || strings.Contains(fp, secret) {
			t.Fatalf("the picture or fingerprint carries %q: %s", secret, doc)
		}
	}

	for name, change := range map[string]func(w *world){
		"secret group var":      func(w *world) { setVars(w, groupSecret+"x", memberSecret, extraSecret) },
		"secret membership var": func(w *world) { setVars(w, groupSecret, memberSecret+"x", extraSecret) },
		"secret extra var":      func(w *world) { setVars(w, groupSecret, memberSecret, extraSecret+"x") },
		"another server key":    func(w *world) { w.key = runrequests.VarsDigestKey([32]byte{4}) },
		"a host's resolved jump host": func(w *world) {
			w.inv.conns = map[string]render.Connection{"web-1": {Bastions: []uuid.UUID{id()}, BastionSource: render.SourceZone}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			setVars(w, groupSecret, memberSecret, extraSecret)
			req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x}
			before := w.picture(t, req).VarsDigest
			change(w)
			if got := w.picture(t, req).VarsDigest; got == before {
				t.Fatalf("%s left the vars digest unchanged", name)
			}
		})
	}
}

// TestVarsDigestIgnoresTheRedactList: the snapshot's redact list only
// repeats values its document holds, so it is no input of its own.
func TestVarsDigestIgnoresTheRedactList(t *testing.T) {
	w := newWorld()
	req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x}
	before := w.picture(t, req).VarsDigest
	w.inv.secrets = []string{"anything"}
	if after := w.picture(t, req).VarsDigest; after != before {
		t.Fatal("the redact list changed the vars digest")
	}
}

func TestVarsDigestNeedsAKey(t *testing.T) {
	w := newWorld()
	w.key = []byte("short")
	if _, err := w.resolver().Picture(t.Context(), id(), runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x}); !errors.Is(err, runrequests.ErrNoVarsKey) {
		t.Fatalf("picture with a short key = %v, want ErrNoVarsKey", err)
	}
}

// TestVarsDigestIsBoundToItsRequest: the served digest carries the request
// id in its input, so two requests over the same values cannot be compared
// to test a planted guess, while one request's digest stays deterministic.
func TestVarsDigestIsBoundToItsRequest(t *testing.T) {
	w := newWorld()
	first := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x}
	second := first
	second.ID = id()
	a := w.picture(t, first).VarsDigest
	if again := w.picture(t, first).VarsDigest; again != a {
		t.Fatalf("one request's vars digest changed between reads: %s then %s", a, again)
	}
	if b := w.picture(t, second).VarsDigest; b == a {
		t.Fatal("two requests over the same values share a vars digest")
	}
}

// TestVarsDigestIgnoresGatheredFacts: facts are gathered by the machine, not
// set or reviewed by a person, so a refresh between approval and claim must
// not change the digest, while a real var still does. The inventory passed
// in is not mutated.
func TestVarsDigestIgnoresGatheredFacts(t *testing.T) {
	w := newWorld()
	req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x}
	setFacts := func(kernel string) {
		w.inv.doc = map[string]any{
			"web": map[string]any{"hosts": []any{"web-1"}},
			"_meta": map[string]any{"hostvars": map[string]any{"web-1": map[string]any{
				"host_var":     "a",
				"pulsix_facts": map[string]any{"kernel": kernel},
			}}},
		}
	}
	setFacts("6.1")
	before := w.picture(t, req).VarsDigest
	setFacts("6.8")
	if after := w.picture(t, req).VarsDigest; after != before {
		t.Fatal("a facts refresh changed the vars digest")
	}
	hv := w.inv.doc["_meta"].(map[string]any)["hostvars"].(map[string]any)["web-1"].(map[string]any)
	if _, ok := hv["pulsix_facts"]; !ok {
		t.Fatal("the digest dropped the facts from the inventory it was given")
	}
	hv["host_var"] = "b"
	if after := w.picture(t, req).VarsDigest; after == before {
		t.Fatal("a var change left the vars digest unchanged")
	}
}

// TestVarsDigestIgnoresRenderedFacts feeds the digest a document the real
// renderer made, so a renamed or moved facts key cannot silently put the
// facts back into the digest: a facts refresh leaves it unchanged, the
// facts are in the rendered document under render.FactsKey, and a real
// host var (the address) still moves the digest.
func TestVarsDigestIgnoresRenderedFacts(t *testing.T) {
	w := newWorld()
	req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x}
	host, zone := id(), id()
	renderWith := func(kernel, address string) {
		t.Helper()
		inv, err := render.Render(render.Input{
			Hosts: []render.Host{{ID: host, ZoneID: zone, Name: "web-1", Address: address, Managed: true, Facts: map[string]any{"kernel": kernel}}},
			Scope: render.Scope{HostIDs: []uuid.UUID{host}},
		})
		if err != nil {
			t.Fatal(err)
		}
		hv := inv.Doc["_meta"].(map[string]any)["hostvars"].(map[string]any)["web-1"].(map[string]any)
		if _, ok := hv[render.FactsKey]; !ok {
			t.Fatalf("the rendered host vars carry no %q: %v", render.FactsKey, hv)
		}
		w.inv.doc = inv.Doc
	}
	renderWith("6.1", "10.0.0.1")
	before := w.picture(t, req).VarsDigest
	renderWith("6.8", "10.0.0.1")
	if after := w.picture(t, req).VarsDigest; after != before {
		t.Fatal("a facts refresh in a rendered document changed the vars digest")
	}
	renderWith("6.8", "10.0.0.2")
	if after := w.picture(t, req).VarsDigest; after == before {
		t.Fatal("an address change left the vars digest unchanged")
	}
}
