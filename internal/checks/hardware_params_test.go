package checks_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
)

func anyList(n int, entry string) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = entry
	}
	return out
}

// TestValidateHardwareParams pins the temperature, smart and raid parameter
// shapes (backlog 3.9): one optional list of strings each, nothing else.
func TestValidateHardwareParams(t *testing.T) {
	type tc struct {
		name   string
		plugin string
		params map[string]any
		ok     bool
	}
	cases := []tc{
		{"temperature empty", "temperature", map[string]any{}, true},
		{"temperature nil", "temperature", nil, true},
		{"temperature sensors", "temperature", map[string]any{"sensors": []any{"coretemp_core_0", "nvme_composite"}}, true},
		{"temperature []string", "temperature", map[string]any{"sensors": []string{"a"}}, true},
		{"temperature 64 entries", "temperature", map[string]any{"sensors": anyList(64, "s")}, true},
		{"temperature 65 entries", "temperature", map[string]any{"sensors": anyList(65, "s")}, false},
		{"temperature max entry", "temperature", map[string]any{"sensors": []any{strings.Repeat("s", 255)}}, true},
		{"temperature long entry", "temperature", map[string]any{"sensors": []any{strings.Repeat("s", 256)}}, false},
		{"temperature empty entry", "temperature", map[string]any{"sensors": []any{""}}, false},
		{"temperature control char", "temperature", map[string]any{"sensors": []any{"a\nb"}}, false},
		{"temperature not a list", "temperature", map[string]any{"sensors": "a"}, false},
		{"temperature non-string entry", "temperature", map[string]any{"sensors": []any{1}}, false},
		{"temperature unknown param", "temperature", map[string]any{"devices": []any{"/dev/sda"}}, false},

		{"smart empty", "smart", map[string]any{}, true},
		{"smart sata", "smart", map[string]any{"devices": []any{"/dev/sda", "/dev/sdb"}}, true},
		{"smart nvme", "smart", map[string]any{"devices": []any{"/dev/nvme0", "/dev/nvme0n1"}}, true},
		{"smart by-id", "smart", map[string]any{"devices": []any{"/dev/disk/by-id/ata-WDC_WD10-1.0:x+y@z"}}, true},
		{"smart megaraid comma", "smart", map[string]any{"devices": []any{"/dev/csmi0,1"}}, true},
		{"smart windows pd", "smart", map[string]any{"devices": []any{"/dev/pd0"}}, true},
		{"smart 64 entries", "smart", map[string]any{"devices": anyList(64, "/dev/sda")}, true},
		{"smart 65 entries", "smart", map[string]any{"devices": anyList(65, "/dev/sda")}, false},
		{"smart relative", "smart", map[string]any{"devices": []any{"sda"}}, false},
		{"smart windows backslash path", "smart", map[string]any{"devices": []any{`\\.\PhysicalDrive0`}}, false},
		{"smart option-like", "smart", map[string]any{"devices": []any{"-a"}}, false},
		{"smart space", "smart", map[string]any{"devices": []any{"/dev/my disk"}}, false},
		{"smart shell metachar", "smart", map[string]any{"devices": []any{"/dev/sda;rm"}}, false},
		{"smart control char", "smart", map[string]any{"devices": []any{"/dev/sda\n"}}, false},
		{"smart empty entry", "smart", map[string]any{"devices": []any{""}}, false},
		{"smart long entry", "smart", map[string]any{"devices": []any{"/dev/" + strings.Repeat("s", 251)}}, false},
		{"smart max entry", "smart", map[string]any{"devices": []any{"/dev/" + strings.Repeat("s", 250)}}, true},
		{"smart not a list", "smart", map[string]any{"devices": "/dev/sda"}, false},
		{"smart unknown param", "smart", map[string]any{"arrays": []any{"md0"}}, false},

		{"raid empty", "raid", map[string]any{}, true},
		{"raid arrays", "raid", map[string]any{"arrays": []any{"md0", "md127"}}, true},
		{"raid 65 entries", "raid", map[string]any{"arrays": anyList(65, "md0")}, false},
		{"raid empty entry", "raid", map[string]any{"arrays": []any{""}}, false},
		{"raid long entry", "raid", map[string]any{"arrays": []any{strings.Repeat("a", 256)}}, false},
		{"raid control char", "raid", map[string]any{"arrays": []any{"md\x000"}}, false},
		{"raid not a list", "raid", map[string]any{"arrays": 3}, false},
		{"raid unknown param", "raid", map[string]any{"sensors": []any{"x"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := checks.ValidateDefinitionShape(c.plugin, c.plugin, c.params, time.Minute)
			if c.ok && err != nil {
				t.Fatalf("ValidateDefinitionShape(%s, %v) = %v, want nil", c.plugin, c.params, err)
			}
			if !c.ok && !errors.Is(err, checks.ErrInvalidInput) {
				t.Fatalf("ValidateDefinitionShape(%s, %v) = %v, want ErrInvalidInput", c.plugin, c.params, err)
			}
		})
	}
}

// TestHardwarePluginsAreAgentSide: the three plugins run on the host's agent.
func TestHardwarePluginsAreAgentSide(t *testing.T) {
	for _, p := range []string{checks.PluginTemperature, checks.PluginSmart, checks.PluginRaid} {
		if checks.IsSynthetic(p) {
			t.Fatalf("IsSynthetic(%s) = true, want false", p)
		}
	}
}
