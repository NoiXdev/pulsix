package checkplugins_test

import (
	"strings"
	"testing"
)

// TestValidateHardwareParams, ported from internal/checks: the temperature,
// smart and raid parameter shapes (backlog 3.9) — one optional list of
// strings each, nothing else.
func TestValidateHardwareParams(t *testing.T) {
	runParamCases(t, []paramCase{
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
		// Only "" is an empty entry for the hardware lists; a blank one is a
		// name like any other (the agent matches it verbatim).
		{"temperature blank entry", "temperature", map[string]any{"sensors": []any{" "}}, true},

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
		{"smart bare /dev/", "smart", map[string]any{"devices": []any{"/dev/"}}, false},
		{"smart parent segment", "smart", map[string]any{"devices": []any{"/dev/../etc/shadow"}}, false},
		{"smart parent segment short", "smart", map[string]any{"devices": []any{"/dev/../x"}}, false},
		{"smart trailing parent segment", "smart", map[string]any{"devices": []any{"/dev/disk/.."}}, false},
		{"smart inner parent segment", "smart", map[string]any{"devices": []any{"/dev/disk/../sda"}}, false},
		{"smart dots inside a name", "smart", map[string]any{"devices": []any{"/dev/disk/by-id/nvme-eui.00..25"}}, true},
		{"smart current segment", "smart", map[string]any{"devices": []any{"/dev/./sda"}}, true},
		{"temperature multibyte over 255 bytes", "temperature", map[string]any{"sensors": []any{strings.Repeat("ä", 128)}}, false},
		{"smart multibyte over 255 bytes", "smart", map[string]any{"devices": []any{"/dev/" + strings.Repeat("ä", 126)}}, false},
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
	})
}
