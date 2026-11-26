package checkplugins_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

func TestParsePHPFPMFastCGI(t *testing.T) {
	sock107 := "/" + strings.Repeat("s", 106)
	path255 := "/" + strings.Repeat("p", 254)
	ok := []struct {
		in   string
		want checkplugins.PHPFPMFastCGI
	}{
		{"fcgi://127.0.0.1:9000/status", checkplugins.PHPFPMFastCGI{Network: "tcp", Address: "127.0.0.1:9000", Path: "/status"}},
		{"fcgi://[::1]:9001/fpm-status", checkplugins.PHPFPMFastCGI{Network: "tcp", Address: "[::1]:9001", Path: "/fpm-status"}},
		{"fcgi://php-fpm.internal:1/a/b_c~d", checkplugins.PHPFPMFastCGI{Network: "tcp", Address: "php-fpm.internal:1", Path: "/a/b_c~d"}},
		{"fcgi://localhost:65535/", checkplugins.PHPFPMFastCGI{Network: "tcp", Address: "localhost:65535", Path: "/"}},
		{"unix:/run/php/php8.3-fpm.sock:/status", checkplugins.PHPFPMFastCGI{Network: "unix", Address: "/run/php/php8.3-fpm.sock", Path: "/status"}},
		{"unix:" + sock107 + ":" + path255, checkplugins.PHPFPMFastCGI{Network: "unix", Address: sock107, Path: path255}},
	}
	for _, tc := range ok {
		if !checkplugins.IsPHPFPMFastCGI(tc.in) {
			t.Errorf("IsPHPFPMFastCGI(%q) = false", tc.in)
		}
		got, err := checkplugins.ParsePHPFPMFastCGI(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParsePHPFPMFastCGI(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	bad := []string{
		"fcgi://127.0.0.1/status",           // no port
		"fcgi://127.0.0.1:0/status",         // port 0
		"fcgi://127.0.0.1:65536/status",     // port out of range
		"fcgi://127.0.0.1:09000/status",     // leading zero
		"fcgi://127.0.0.1:9000",             // no path
		"fcgi://127.0.0.1:9000/status?json", // query
		"fcgi://127.0.0.1:9000/status#x",    // fragment
		"fcgi://127.0.0.1:9000/status.php",  // a dot in the path
		"fcgi://127.0.0.1:9000/x.php/status",
		"fcgi://127.0.0.1:9000/st atus",
		"fcgi://127.0.0.1:9000/st%41tus",
		"fcgi://127.0.0.1:9000/a\nb",
		"fcgi://u@127.0.0.1:9000/status",
		"fcgi://::1:9000/status",     // IPv6 without brackets
		"fcgi://[::1]/status",        // bracketed without port
		"fcgi://[127.0.0.1]:9000/st", // brackets around IPv4
		"fcgi://bad_host!:9000/status",
		"fcgi://:9000/status",
		"fcgi:///status",
		"FCGI://127.0.0.1:9000/status",
		"unix:/run/php/fpm.sock",           // no path
		"unix:/run/php/fpm.sock:",          // empty path
		"unix:/run/php/fpm.sock:status",    // relative path
		"unix:run/php/fpm.sock:/status",    // relative socket
		"unix:/run/a:b/fpm.sock:/status",   // a colon in the socket: the path would be "b/fpm.sock:/status"
		"unix:/run/php/fpm.sock:/a.b",      // a dot in the path
		"unix:/run/php/fpm.sock:/a?json",   // query
		"unix:/run/php/\x00.sock:/status",  // NUL
		"unix:/run/php/\n.sock:/status",    // control
		"unix:" + sock107 + "x:/status",    // socket of 108 bytes
		"unix:/s:" + path255 + "p",         // path of 256 bytes
		"unix:///run/php/fpm.sock:/status", // URL spelling, not a path
		"unix::/status",
		"unix:",
		"http://127.0.0.1/status",
	}
	for _, in := range bad {
		if _, err := checkplugins.ParsePHPFPMFastCGI(in); err == nil {
			t.Errorf("ParsePHPFPMFastCGI(%q) accepted", in)
		}
	}
	for _, in := range []string{"http://127.0.0.1/status", "https://h/x", "/run/php/fpm.sock", "tcp://127.0.0.1:9000"} {
		if checkplugins.IsPHPFPMFastCGI(in) {
			t.Errorf("IsPHPFPMFastCGI(%q) = true", in)
		}
	}
}

// TestPHPFPMPoolFormat: the phpfpmPool format is statusURL or a FastCGI
// entry, and its refusal of a FastCGI entry says what is wrong with it.
func TestPHPFPMPoolFormat(t *testing.T) {
	f := checkplugins.FormatPHPFPMPool
	for _, in := range []string{"http://127.0.0.1/status", "https://h:8443/fpm?full", "fcgi://127.0.0.1:9000/status", "unix:/run/php/fpm.sock:/status"} {
		if !f.Valid(in) {
			t.Errorf("Valid(%q) = false", in)
		}
	}
	for _, in := range []string{"ftp://x/", "http://u:p@h/", "fcgi://127.0.0.1/status", "unix:/run/php/fpm.sock", ""} {
		if f.Valid(in) {
			t.Errorf("Valid(%q) = true", in)
		}
	}
}
