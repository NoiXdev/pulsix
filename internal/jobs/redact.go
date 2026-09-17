// The claim's redact list (final review I4): every secret value a claim
// reveals is named in workerpb.Job.redact, so the worker can remove it from
// every run event before the event leaves the worker. Viewers with
// jobs.read see run logs; they must never see a credential, a secret extra
// var or a secret inventory var a task happened to print.
package jobs

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// redactSet collects secret values once each, in insertion order.
type redactSet struct {
	seen map[string]bool
	list []string
}

func (r *redactSet) add(values ...string) {
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	for _, v := range values {
		if v == "" || r.seen[v] {
			continue
		}
		r.seen[v] = true
		r.list = append(r.list, v)
	}
}

// addSecret adds every non-empty part of a revealed credential secret.
func (r *redactSet) addSecret(s credentials.Secret) {
	r.add(s.PrivateKey, s.Password, s.Token)
}

// addSecretJSON adds a claim credential string (Secret JSON). A value that
// is not Secret JSON is added whole: whatever the claim carries under a
// credential key is secret.
func (r *redactSet) addSecretJSON(raw string) {
	if raw == "" {
		return
	}
	var s credentials.Secret
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		r.add(raw)
		return
	}
	r.addSecret(s)
}

// secretPayloadKeys are the kind-payload keys whose string values are
// revealed Secret JSON (the one-time login, the rotating credential, the
// revocation's login, the probe credential and each hop's credential).
var secretPayloadKeys = map[string]bool{
	"login": true, "old_credential": true, "login_credential": true, "credential": true,
}

// addKindPayload walks a kind payload document and adds the secrets under
// secretPayloadKeys, at any depth (the bastions list included).
func (r *redactSet) addKindPayload(payload string) {
	if payload == "" {
		return
	}
	var doc any
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return
	}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			// Sorted keys: map order is random, and the claim's redact list
			// must come out the same for the same payload.
			for _, k := range slices.Sorted(maps.Keys(t)) {
				e := t[k]
				if s, ok := e.(string); ok && secretPayloadKeys[k] {
					r.addSecretJSON(s)
					continue
				}
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(doc)
}

// addExtraVarSecrets adds the values of a template's secret-typed extra
// vars. Without a schema no extra var is typed secret.
func (r *redactSet) addExtraVarSecrets(schemaRaw, varsRaw json.RawMessage) error {
	if len(schemaRaw) == 0 || string(schemaRaw) == "null" || len(varsRaw) == 0 {
		return nil
	}
	var sc schema.Schema
	if err := json.Unmarshal(schemaRaw, &sc); err != nil {
		return fmt.Errorf("template extra vars schema: %w", err)
	}
	var vars map[string]any
	if err := json.Unmarshal(varsRaw, &vars); err != nil {
		return fmt.Errorf("%w: extra vars are not a JSON object", ErrInvalidInput)
	}
	_, err := schema.MapSecrets(sc.Fields, vars, func(_ string, v any) (any, error) {
		if s, ok := v.(string); ok {
			r.add(s)
		}
		return v, nil
	})
	return err
}
