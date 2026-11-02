package backup

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func sampleHeader() Header {
	return Header{
		FormatVersion:   FormatVersion,
		CreatedAt:       time.Date(2026, 11, 2, 3, 0, 0, 123456789, time.UTC),
		Kind:            "scheduled",
		PulsixVersion:   "0.9.0",
		SchemaVersion:   74,
		IncludesMetrics: true,
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	want := sampleHeader()

	var buf bytes.Buffer
	if err := WriteHeader(&buf, want); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "PULSIXBAK1\n") {
		t.Fatalf("header does not start with the magic line: %q", buf.String())
	}
	if n := strings.Count(buf.String(), "\n"); n != 2 {
		t.Fatalf("header has %d lines, want magic + one JSON line", n)
	}

	got, err := ReadHeader(&buf)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	got.CreatedAt = want.CreatedAt
	if got != want {
		t.Errorf("header = %+v, want %+v", got, want)
	}
}

func TestWriteHeaderStampsFormatVersion(t *testing.T) {
	h := sampleHeader()
	h.FormatVersion = 0

	var buf bytes.Buffer
	if err := WriteHeader(&buf, h); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	got, err := ReadHeader(&buf)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if got.FormatVersion != FormatVersion {
		t.Errorf("FormatVersion = %d, want %d", got.FormatVersion, FormatVersion)
	}
}

func TestReadHeaderRejects(t *testing.T) {
	long := "PULSIXBAK1\n" + strings.Repeat("x", 5000) + "\n"
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"not a backup", "hello", ErrNotABackup},
		{"empty", "", ErrNotABackup},
		{"wrong magic", "PULSIXBAK9\n{}\n", ErrNotABackup},
		{"magic only", "PULSIXBAK1\n", ErrCorrupt},
		{"header not terminated", "PULSIXBAK1\n{\"kind\":\"manual\"}", ErrCorrupt},
		{"header over 4 KiB", long, ErrCorrupt},
		{"header not json", "PULSIXBAK1\nnot json\n", ErrCorrupt},
		{"future format version", "PULSIXBAK1\n{\"format_version\":2}\n", ErrNotABackup},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadHeader(strings.NewReader(tc.in))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestReadHeaderLeavesPayloadUnread(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHeader(&buf, sampleHeader()); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("PAYLOAD")

	if _, err := ReadHeader(&buf); err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	rest, err := io.ReadAll(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != "PAYLOAD" {
		t.Errorf("rest = %q, want the payload untouched", rest)
	}
}
