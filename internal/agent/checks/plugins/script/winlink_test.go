package script

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// reparseBuffer builds a REPARSE_DATA_BUFFER for tag with substitute name
// sub and a print name that differs from it, so a reader of the wrong
// name is caught.
func reparseBuffer(tag uint32, sub string, flags uint32) []byte {
	enc := func(s string) []byte {
		var b []byte
		for _, u := range utf16.Encode([]rune(s)) {
			b = binary.LittleEndian.AppendUint16(b, u)
		}
		return b
	}
	subB, printB := enc(sub), enc("PRINT-NAME")
	var names []byte
	names = append(append(names, subB...), printB...)
	var body []byte
	body = binary.LittleEndian.AppendUint16(body, 0)
	body = binary.LittleEndian.AppendUint16(body, uint16(len(subB)))
	body = binary.LittleEndian.AppendUint16(body, uint16(len(subB)))
	body = binary.LittleEndian.AppendUint16(body, uint16(len(printB)))
	if tag == reparseTagSymlink {
		body = binary.LittleEndian.AppendUint32(body, flags)
	}
	body = append(body, names...)
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, tag)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(body)))
	b = binary.LittleEndian.AppendUint16(b, 0)
	return append(b, body...)
}

func TestParseReparse(t *testing.T) {
	cases := []struct {
		name     string
		buf      []byte
		target   string
		relative bool
		err      bool
	}{
		{"absolute symlink", reparseBuffer(reparseTagSymlink, `\??\C:\Windows\System32`, 0), `\??\C:\Windows\System32`, false, false},
		{"relative symlink", reparseBuffer(reparseTagSymlink, `..\Windows\System32`, symlinkFlagRelative), `..\Windows\System32`, true, false},
		{"junction", reparseBuffer(reparseTagMountPoint, `\??\C:\Windows\System32\`, 0), `\??\C:\Windows\System32\`, false, false},
		{"another tag", reparseBuffer(0x8000001b, `x`, 0), "", false, true},
		{"truncated header", []byte{0x0c, 0, 0}, "", false, true},
		{"name beyond the buffer", reparseBuffer(reparseTagSymlink, `C:\x`, 0)[:22], "", false, true},
	}
	for _, tc := range cases {
		target, relative, err := parseReparse(tc.buf)
		if (err != nil) != tc.err || target != tc.target || relative != tc.relative {
			t.Errorf("%s: parseReparse = %q, %v, %v; want %q, %v, error %v", tc.name, target, relative, err, tc.target, tc.relative, tc.err)
		}
	}
}

// TestWindowsLinkPath: a relative target goes on from the link's folder
// (a rooted one on its drive); an absolute one must name a drive in the
// \??\ form; anything else (a volume, the network, a drive-relative or
// stream path) is not followed.
func TestWindowsLinkPath(t *testing.T) {
	dir := `C:\links`
	cases := []struct {
		target   string
		relative bool
		want     string // "" is an error
	}{
		{`\??\C:\Windows\System32`, false, `C:\Windows\System32`},
		{`\??\D:\data\`, false, `D:\data\`},
		{`..\Windows\System32`, true, `C:\links\..\Windows\System32`},
		{`System32`, true, `C:\links\System32`},
		{`\Windows\System32`, true, `C:\Windows\System32`},
		{`\??\Volume{0a}\`, false, ""},
		{`\??\UNC\host\share\x`, false, ""},
		{`C:\Windows`, false, ""},
		{`\??\C:`, false, ""},
		{`D:x`, true, ""},
		{`\\host\share\x`, true, ""},
		{`x:stream`, true, ""},
	}
	for _, tc := range cases {
		got, err := windowsLinkPath(dir, tc.target, tc.relative)
		switch {
		case tc.want == "" && err == nil:
			t.Errorf("windowsLinkPath(%q, %v) = %q, want an error", tc.target, tc.relative, got)
		case tc.want != "" && (err != nil || got != tc.want):
			t.Errorf("windowsLinkPath(%q, %v) = %q, %v; want %q", tc.target, tc.relative, got, err, tc.want)
		}
	}
}
