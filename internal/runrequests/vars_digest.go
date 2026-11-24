package runrequests

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
)

// varsKeyInfo labels the HKDF derivation of the vars digest key. Changing
// it changes every vars digest, and with it every fingerprint.
const varsKeyInfo = "pulsix run request vars digest v1"

// ErrNoVarsKey means the resolver was wired without a vars digest key; a
// picture is then refused rather than digested without one.
var ErrNoVarsKey = errors.New("run request vars digest: no key")

// VarsDigestKey derives the vars digest key from the instance master key:
// HKDF-SHA256(masterKey, no salt, "pulsix run request vars digest v1", 32
// bytes), the shape of setup.Token and backup.DeriveKey under its own
// label. It is derived rather than configured, so every manager of the
// instance computes the same digests.
func VarsDigestKey(masterKey [32]byte) []byte {
	key, err := hkdf.Key(sha256.New, masterKey[:], nil, varsKeyInfo, 32)
	if err != nil {
		// Only a malformed length or hash can fail, and both are constants.
		panic(fmt.Sprintf("derive vars digest key: %v", err))
	}
	return key
}

// factsKey is the hostvar the renderer files a host's gathered facts under.
const factsKey = "pulsix_facts"

// withoutFacts returns doc with every host's gathered facts removed, on a
// copy of the parts it changes: facts are gathered by the machine, not set
// or reviewed by a person, so a refresh between approval and claim must not
// fail the run. doc itself is never mutated.
func withoutFacts(doc map[string]any) map[string]any {
	meta, ok := doc["_meta"].(map[string]any)
	if !ok {
		return doc
	}
	hostvars, ok := meta["hostvars"].(map[string]any)
	if !ok {
		return doc
	}
	stripped := make(map[string]any, len(hostvars))
	for host, v := range hostvars {
		vars, ok := v.(map[string]any)
		if !ok {
			stripped[host] = v
			continue
		}
		cp := make(map[string]any, len(vars))
		for k, val := range vars {
			if k != factsKey {
				cp[k] = val
			}
		}
		stripped[host] = cp
	}
	metaCopy := make(map[string]any, len(meta))
	for k, v := range meta {
		metaCopy[k] = v
	}
	metaCopy["hostvars"] = stripped
	out := make(map[string]any, len(doc))
	for k, v := range doc {
		out[k] = v
	}
	out["_meta"] = metaCopy
	return out
}

// varsDigest is the HMAC-SHA256, under the server-side VarsKey, of the
// values the run would receive: the targets' rendered inventory (group and
// membership vars, parents included, secrets revealed, and each host's
// connection) and the template's extra vars (secrets opened). Gathered
// host facts (pulsix_facts) are left out, see withoutFacts: a machine
// refreshes them, no person sets or reviews them. Any change to any other
// value changes it, a secret included, while the digest the browser
// sees cannot be tested against a guessed secret without the key. The
// request id is part of the input, so the digest of one request cannot be
// compared with another's over the same values (a guess planted on a copy
// template); the approval and the claim guard read the same request, so
// they still agree.
func (r *Resolver) varsDigest(ctx context.Context, orgID, requestID, templateID uuid.UUID, targets []uuid.UUID) (string, error) {
	if len(r.src.VarsKey) < 32 {
		return "", ErrNoVarsKey
	}
	var inv *render.Inventory
	if len(targets) > 0 {
		rendered, err := r.src.Vars.InventoryVars(ctx, orgID, targets)
		if err != nil {
			return "", fmt.Errorf("render target inventory: %w", err)
		}
		// The redact list only repeats values the document holds.
		rendered.Secrets = nil
		rendered.Doc = withoutFacts(rendered.Doc)
		inv = &rendered
	}
	extra, err := r.src.Vars.ExtraVars(ctx, orgID, templateID)
	if err != nil {
		return "", fmt.Errorf("read template extra vars: %w", mapNotFound(err))
	}
	doc, err := json.Marshal(struct {
		Request   string            `json:"request"`
		Inventory *render.Inventory `json:"inventory"`
		ExtraVars map[string]any    `json:"extra_vars"`
	}{requestID.String(), inv, extra})
	if err != nil {
		return "", fmt.Errorf("encode vars digest: %w", err)
	}
	mac := hmac.New(sha256.New, r.src.VarsKey)
	mac.Write(doc)
	return hex.EncodeToString(mac.Sum(nil)), nil
}
