package notify

import (
	"fmt"
	"slices"
	"strings"
)

// webhookURL is the sealed URL and display form a webhook channel stores:
// the one a preset builds (Endpointed, backlog 4.8), else the submitted
// one, else on update the stored one — unless the preset changed, since a
// URL belongs to one service and a sealed one cannot be re-checked.
func (s *Service) webhookURL(aad []byte, preset Preset, in WebhookInput, settings PresetSettings, stored webhookConfig, update bool, curPreset string) (sealed []byte, display string, err error) {
	raw := ""
	ep, built := preset.(Endpointed)
	switch {
	case built && in.URL != nil:
		return nil, "", fmt.Errorf("%w: a %s channel's URL is set by Pulsix; leave the URL out", ErrInvalidInput, in.Preset)
	case built:
		raw = ep.Endpoint(settings)
	case in.URL != nil:
		raw = *in.URL
	case update && in.Preset != curPreset:
		return nil, "", fmt.Errorf("%w: changing a channel's preset needs the URL again", ErrInvalidInput)
	case update:
		return stored.URLSealed, stored.URLDisplay, nil
	default:
		return nil, "", fmt.Errorf("%w: a webhook channel needs a url", ErrInvalidInput)
	}
	if display, err = checkURL(raw); err != nil {
		return nil, "", err
	}
	if !built {
		if err := checkPresetURL(preset, raw); err != nil {
			return nil, "", err
		}
	}
	if sealed, err = s.d.Box.SealAAD([]byte(strings.TrimSpace(raw)), aad); err != nil {
		return nil, "", fmt.Errorf("seal webhook url: %w", err)
	}
	return sealed, display, nil
}

// presetSecrets seals a Credentialed preset's secrets: each submitted one
// is checked and sealed, each left out keeps its stored value when keep
// holds (the same preset on update), and every one the preset names is
// required. A channel whose preset takes no secrets is given none.
// Errors name a secret, never its value.
//
// rebound names the setting whose change voided the kept secrets
// (secretsKept), for the error.
func (s *Service) presetSecrets(aad []byte, preset Preset, in map[string]string, stored []storedHeader, keep bool, rebound string) ([]storedHeader, error) {
	c, ok := preset.(Credentialed)
	if !ok {
		if len(in) > 0 {
			return nil, fmt.Errorf("%w: this channel takes no secrets", ErrInvalidInput)
		}
		return nil, nil
	}
	names := c.SecretNames()
	for name := range in {
		if !slices.Contains(names, name) {
			return nil, fmt.Errorf("%w: unknown secret %q; this channel takes %s", ErrInvalidInput, oneLine(Clip(name, 40)), strings.Join(names, ", "))
		}
	}
	out := make([]storedHeader, 0, len(names))
	for _, name := range names {
		if v := in[name]; v != "" {
			if err := c.CheckSecret(name, v); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
			}
			sealed, err := s.d.Box.SealAAD([]byte(v), aad)
			if err != nil {
				return nil, fmt.Errorf("seal secret %s: %w", name, err)
			}
			out = append(out, storedHeader{Name: name, ValueSealed: sealed})
			continue
		}
		i := slices.IndexFunc(stored, func(h storedHeader) bool { return h.Name == name })
		if rebound != "" && i >= 0 {
			return nil, fmt.Errorf("%w: changing %s needs the secret %s again", ErrInvalidInput, rebound, name)
		}
		if !keep || i < 0 {
			return nil, fmt.Errorf("%w: the secret %s is required", ErrInvalidInput, name)
		}
		out = append(out, stored[i])
	}
	return out, nil
}

// secretsKept decides whether a preset's stored secrets may be kept: only
// on an update of the same preset (keep), and not when the setting they
// belong to changed (SecretScoped: Twilio's account SID, review M2) —
// then rebound names that setting.
func secretsKept(preset Preset, keep bool, stored, now PresetSettings) (bool, string) {
	sc, ok := preset.(SecretScoped)
	if !ok || !keep {
		return keep, ""
	}
	name, value := sc.SecretScope(now)
	if _, was := sc.SecretScope(stored); was != value {
		return false, name
	}
	return true, ""
}
