package backup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// Verify reads a whole backup file and returns its manifest once everything
// checks out: the encryption authenticates to the last byte, the framing is
// intact, every table's rows match the manifest's count, and the plaintext
// header equals the header inside the manifest. It changes nothing, so a
// restore runs it before it drops anything.
//
// The plaintext header is not authenticated; only the manifest's copy is.
// Callers decide on the returned Manifest.Header, never on ReadHeader's.
func Verify(r io.Reader, masterKey [32]byte) (Manifest, error) {
	plain, err := ReadHeader(r)
	if err != nil {
		return Manifest{}, err
	}
	dec, err := Decrypt(r, masterKey)
	if err != nil {
		return Manifest{}, err
	}
	fr := NewFrameReader(dec)
	var seen []TableCount
	for {
		t, err := fr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, err
		}
		rows, err := countRows(t.Data)
		if err != nil {
			return Manifest{}, fmt.Errorf("table %s: %w", t.Name, err)
		}
		seen = append(seen, TableCount{Name: t.Name, Rows: rows})
	}
	m := fr.Manifest()
	if !sameHeader(plain, m.Header) {
		return Manifest{}, fmt.Errorf("%w: the plaintext header does not match the manifest", ErrCorrupt)
	}
	if err := checkTables(seen, m.Tables); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// countRows counts the rows of one table's COPY text: one line each, every
// line ending in a newline (COPY escapes newlines inside values).
func countRows(r io.Reader) (int64, error) {
	var rows int64
	var last byte = '\n'
	buf := make([]byte, 64<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			rows += int64(bytes.Count(buf[:n], []byte{'\n'}))
			last = buf[n-1]
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if last != '\n' {
		return 0, fmt.Errorf("%w: the last row is cut short", ErrCorrupt)
	}
	return rows, nil
}

// checkTables requires the tables read from the stream to be exactly the
// manifest's, in its order, each with its row count.
func checkTables(seen, manifest []TableCount) error {
	if len(seen) != len(manifest) {
		return fmt.Errorf("%w: the stream holds %d tables, the manifest lists %d", ErrCorrupt, len(seen), len(manifest))
	}
	names := make(map[string]bool, len(seen))
	for i, s := range seen {
		if names[s.Name] {
			return fmt.Errorf("%w: table %s appears twice", ErrCorrupt, s.Name)
		}
		names[s.Name] = true
		if s != manifest[i] {
			return fmt.Errorf("%w: the stream holds %s with %d rows, the manifest lists %s with %d",
				ErrCorrupt, s.Name, s.Rows, manifest[i].Name, manifest[i].Rows)
		}
	}
	return nil
}

// sameHeader compares two headers field by field; times compare as instants.
func sameHeader(a, b Header) bool {
	return a.FormatVersion == b.FormatVersion &&
		a.CreatedAt.Equal(b.CreatedAt) &&
		a.Kind == b.Kind &&
		a.PulsixVersion == b.PulsixVersion &&
		a.SchemaVersion == b.SchemaVersion &&
		a.IncludesMetrics == b.IncludesMetrics
}
