package checks

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Bounds of the hardware plugins' parameters (backlog 3.9). The agent
// trusts params that pass here, so these and the plugins under
// internal/agent/checks/plugins/ (temperature, smart, raid) agree.
const (
	maxHardwareListEntries = 64
	maxHardwareEntryLen    = 255
)

// smartDevicePattern is the shape of a device path smartctl is handed:
// always under /dev/ (smartctl names devices /dev/sdX, /dev/nvmeN, /dev/pdN
// and /dev/csmiN,M on Windows as well) and limited to characters that carry
// no meaning to a shell, an option parser or a path separator trick.
var smartDevicePattern = regexp.MustCompile(`^/dev/[A-Za-z0-9._/,:+@-]+$`)

// validateHardwareParams checks the temperature (sensors), smart (devices)
// and raid (arrays) parameters: one optional list of strings each.
func validateHardwareParams(plugin string) func(map[string]any) error {
	param := map[string]string{PluginTemperature: "sensors", PluginSmart: "devices", PluginRaid: "arrays"}[plugin]
	return func(params map[string]any) error {
		for k := range params {
			if k != param {
				return fmt.Errorf("%w: %s: unknown parameter %q", ErrInvalidInput, plugin, k)
			}
		}
		v, ok := params[param]
		if !ok {
			return nil
		}
		list, isList := stringList(v)
		if !isList {
			return fmt.Errorf("%w: %s: %s must be a list of strings", ErrInvalidInput, plugin, param)
		}
		if len(list) > maxHardwareListEntries {
			return fmt.Errorf("%w: %s: %s must have at most %d entries", ErrInvalidInput, plugin, param, maxHardwareListEntries)
		}
		for _, e := range list {
			if err := validateHardwareEntry(plugin, param, e); err != nil {
				return err
			}
		}
		return nil
	}
}

func validateHardwareEntry(plugin, param, e string) error {
	if e == "" {
		return fmt.Errorf("%w: %s: %s must not contain an empty entry", ErrInvalidInput, plugin, param)
	}
	if len(e) > maxHardwareEntryLen {
		return fmt.Errorf("%w: %s: %s entries must be at most %d bytes", ErrInvalidInput, plugin, param, maxHardwareEntryLen)
	}
	if strings.ContainsFunc(e, unicode.IsControl) {
		return fmt.Errorf("%w: %s: %s entries must not contain control characters", ErrInvalidInput, plugin, param)
	}
	if plugin == PluginSmart && !smartDevicePattern.MatchString(e) {
		return fmt.Errorf("%w: smart: devices entries must start with /dev/ and contain only letters, digits and . _ / , : + @ -", ErrInvalidInput)
	}
	return nil
}
