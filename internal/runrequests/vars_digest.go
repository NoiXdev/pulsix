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

// varsDigest is the HMAC-SHA256, under the server-side VarsKey, of the
// values the run would receive: the targets' rendered inventory (group and
// membership vars, parents included, secrets revealed, and each host's
// connection) and the template's extra vars (secrets opened). Any change
// to a value changes it, a secret included, while the digest the browser
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
