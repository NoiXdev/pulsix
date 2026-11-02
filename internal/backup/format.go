// Package backup defines the Pulsix backup file: a one-line plaintext header
// followed by an age-encrypted, gzip-compressed stream of table frames.
package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// FormatVersion is the version of the file layout described in this package.
const FormatVersion = 1

// magic starts every backup file.
const magic = "PULSIXBAK1\n"

// maxHeaderLen bounds the JSON header line, newline included.
const maxHeaderLen = 4 << 10

var (
	// ErrNotABackup means the input does not start with the backup magic, or
	// carries a format version this build does not know.
	ErrNotABackup = errors.New("backup: not a Pulsix backup file")
	// ErrWrongKey means the file is intact but was encrypted under another
	// master key.
	ErrWrongKey = errors.New("backup: wrong master key")
	// ErrCorrupt means the file is damaged: bad framing, a missing manifest,
	// a truncated stream or a failed authentication check.
	ErrCorrupt = errors.New("backup: corrupt backup file")
)

// Header is the only plaintext part of a backup file. It is written as JSON
// on one line right after the magic line, so a listing can describe a file
// without decrypting it.
type Header struct {
	FormatVersion   int       `json:"format_version"`
	CreatedAt       time.Time `json:"created_at"`
	Kind            string    `json:"kind"` // "scheduled" or "manual"
	PulsixVersion   string    `json:"pulsix_version"`
	SchemaVersion   int64     `json:"schema_version"`
	IncludesMetrics bool      `json:"includes_metrics"`
}

// Manifest is the last frame of the encrypted stream. A stream without one
// was cut short.
type Manifest struct {
	Header    Header          `json:"header"`
	Tables    []TableCount    `json:"tables"`
	Sequences []SequenceValue `json:"sequences"`
}

// TableCount is the number of rows dumped from one table.
type TableCount struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

// SequenceValue is the state of one sequence at dump time.
type SequenceValue struct {
	Name      string `json:"name"`
	LastValue int64  `json:"last_value"`
	IsCalled  bool   `json:"is_called"`
}

// WriteHeader writes the magic line and the header as one JSON line. The
// format version is always the current FormatVersion.
func WriteHeader(w io.Writer, h Header) error {
	h.FormatVersion = FormatVersion
	line, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("encode backup header: %w", err)
	}
	if len(line)+1 > maxHeaderLen {
		return fmt.Errorf("encode backup header: %d bytes exceed the %d byte limit", len(line)+1, maxHeaderLen)
	}
	out := make([]byte, 0, len(magic)+len(line)+1)
	out = append(out, magic...)
	out = append(out, line...)
	out = append(out, '\n')
	if _, err := w.Write(out); err != nil {
		return fmt.Errorf("write backup header: %w", err)
	}
	return nil
}

// ReadHeader reads the magic line and the header line, and nothing after
// them: the encrypted payload stays unread in r. It reads at most 4 KiB.
func ReadHeader(r io.Reader) (Header, error) {
	got := make([]byte, len(magic))
	if _, err := io.ReadFull(r, got); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Header{}, ErrNotABackup
		}
		return Header{}, fmt.Errorf("read backup magic: %w", err)
	}
	if string(got) != magic {
		return Header{}, ErrNotABackup
	}

	// One byte at a time, so nothing past the newline is consumed from an
	// unbuffered source.
	var line bytes.Buffer
	one := make([]byte, 1)
	for {
		if line.Len() >= maxHeaderLen {
			return Header{}, fmt.Errorf("%w: header exceeds %d bytes", ErrCorrupt, maxHeaderLen)
		}
		if _, err := io.ReadFull(r, one); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return Header{}, fmt.Errorf("%w: header line is cut short", ErrCorrupt)
			}
			return Header{}, fmt.Errorf("read backup header: %w", err)
		}
		if one[0] == '\n' {
			break
		}
		line.WriteByte(one[0])
	}

	var h Header
	if err := json.Unmarshal(line.Bytes(), &h); err != nil {
		return Header{}, fmt.Errorf("%w: decode header: %v", ErrCorrupt, err)
	}
	if h.FormatVersion != FormatVersion {
		return Header{}, fmt.Errorf("%w: unsupported format version %d", ErrNotABackup, h.FormatVersion)
	}
	return h, nil
}
