package script

import (
	"fmt"
	"strings"
	"testing"
)

func TestNagiosStatus(t *testing.T) {
	tests := []struct {
		res  runResult
		want status
	}{
		{runResult{exited: true, exitCode: 0}, statusOK},
		{runResult{exited: true, exitCode: 1}, statusWarn},
		{runResult{exited: true, exitCode: 2}, statusCrit},
		{runResult{exited: true, exitCode: 3}, statusUnknown},
		{runResult{exited: true, exitCode: 4}, statusUnknown},
		{runResult{exited: true, exitCode: 255}, statusUnknown},
		{runResult{exited: true, exitCode: -1}, statusUnknown},
		{runResult{exited: false, exitCode: 0}, statusUnknown},
	}
	for _, tt := range tests {
		if got := nagiosStatus(tt.res); got != tt.want {
			t.Errorf("nagiosStatus(exited %v, code %d) = %s, want %s", tt.res.exited, tt.res.exitCode, got, tt.want)
		}
	}
}

func TestParseNagios(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		message string
		values  []itemValue
	}{
		{name: "message only", out: "DISK OK - free space: / 3326 MB\n", message: "DISK OK - free space: / 3326 MB"},
		{name: "empty", out: "", message: ""},
		{name: "CRLF", out: "OK\r\nmore\r\n", message: "OK"},
		{
			name: "perfdata on line one", out: "DISK OK | /=2643MB;5948;5958;0;5968 load=0.5",
			message: "DISK OK", values: []itemValue{{"c:/", 2643 * 1024 * 1024}, {"c:load", 0.5}},
		},
		{
			name:    "long output with perfdata after a later pipe, continuing on following lines",
			out:     "OK - three disks | a=1\nsda fine\nsdb fine | b=2\nc=3 d=4\n",
			message: "OK - three disks",
			values:  []itemValue{{"c:a", 1}, {"c:b", 2}, {"c:c", 3}, {"c:d", 4}},
		},
		{
			name: "a later line without a pipe before it is long text", out: "OK\nx=1\n",
			message: "OK",
		},
		{
			name: "quoted labels with spaces and escaped quotes", out: "OK | 'free space'=5 'it''s'=6 ''=7",
			message: "OK", values: []itemValue{{"c:free space", 5}, {"c:it's", 6}},
		},
		{
			name: "units", out: "OK | t=2s ms=1500ms us=250us p=99.5% b=10B kb=1KB mb=1MB gb=1GB tb=1TB c=7c n=-3",
			message: "OK", values: []itemValue{
				{"c:t", 2}, {"c:ms", 1.5}, {"c:us", 0.00025}, {"c:p", 99.5}, {"c:b", 10},
				{"c:kb", 1024}, {"c:mb", 1 << 20}, {"c:gb", 1 << 30}, {"c:tb", 1 << 40}, {"c:c", 7}, {"c:n", -3},
			},
		},
		{
			name: "garbage skipped", out: "OK | novalue= =3 x=U y=abc z=1.2.3 e=1e3 'open=4 w=5;6;7 inf=" + strings.Repeat("9", 400) + " last=8",
			message: "OK", values: []itemValue{{"c:w", 5}, {"c:last", 8}},
		},
		{
			name: "a value that overflows once normalised", out: "OK | big=" + strings.Repeat("9", 300) + "TB small=1",
			message: "OK", values: []itemValue{{"c:small", 1}},
		},
		{
			name: "an unknown unit is taken as is", out: "OK | temp=45C fan=1200rpm",
			message: "OK", values: []itemValue{{"c:temp", 45}, {"c:fan", 1200}},
		},
		{
			name: "an unquoted label with spaces never swallows the next entry", out: "OK | a b=1 c=2",
			message: "OK", values: []itemValue{{"c:b", 1}, {"c:c", 2}},
		},
		{
			name: "duplicate label keeps the first", out: "OK | a=1 a=2",
			message: "OK", values: []itemValue{{"c:a", 1}},
		},
		{
			name: "message is everything before the first pipe", out: "OK | a=1 | b=2",
			message: "OK", values: []itemValue{{"c:a", 1}, {"c:b", 2}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, values := parseNagios("c", []byte(tt.out))
			if msg != tt.message {
				t.Errorf("message = %q, want %q", msg, tt.message)
			}
			if fmt.Sprint(values) != fmt.Sprint(tt.values) {
				t.Errorf("values = %v\nwant     %v", values, tt.values)
			}
		})
	}
}

func TestParseNagiosValueCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("OK |")
	for i := range maxValues + 1 {
		fmt.Fprintf(&b, " v%d=%d", i, i)
	}
	_, values := parseNagios("c", []byte(b.String()))
	if len(values) != maxValues {
		t.Fatalf("kept %d values, want %d", len(values), maxValues)
	}
	if values[maxValues-1].item != fmt.Sprintf("c:v%d", maxValues-1) {
		t.Errorf("last kept = %v, want v%d", values[maxValues-1], maxValues-1)
	}
}

func TestParseNagiosHostileOutput(t *testing.T) {
	// 64 KiB of pipes, quotes and equals signs: bounded, and every item reportable.
	out := []byte(strings.Repeat("|'=''\x00\xff=1 ", maxOutput/12))
	_, values := parseNagios("c", out)
	if len(values) > maxValues {
		t.Errorf("kept %d values, more than %d", len(values), maxValues)
	}
}
