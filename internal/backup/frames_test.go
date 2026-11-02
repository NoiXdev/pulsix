package backup

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"
)

func sampleManifest() Manifest {
	return Manifest{
		Header: sampleHeader(),
		Tables: []TableCount{{Name: "empty_table", Rows: 0}, {Name: "users", Rows: 3}},
		Sequences: []SequenceValue{
			{Name: "audit_seq", LastValue: 42, IsCalled: true},
			{Name: "fresh_seq", LastValue: 1, IsCalled: false},
		},
	}
}

// copyText is three rows of COPY text format: a tab separator, a \N null and
// an escaped backslash.
const copyText = "1\talice\t\\N\n2\tbob\\\\smith\tx\n3\t\\N\t\\N\n"

func writeSampleFrames(t *testing.T, w io.Writer) {
	t.Helper()
	fw := NewFrameWriter(w)
	if err := fw.Begin("empty_table", []string{"id"}); err != nil {
		t.Fatal(err)
	}
	if err := fw.End(0); err != nil {
		t.Fatal(err)
	}
	if err := fw.Begin("users", []string{"id", "name", "note"}); err != nil {
		t.Fatal(err)
	}
	if n, err := fw.Write([]byte(copyText)); err != nil || n != len(copyText) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if err := fw.End(3); err != nil {
		t.Fatal(err)
	}
	if err := fw.Manifest(sampleManifest()); err != nil {
		t.Fatal(err)
	}
}

func readTable(t *testing.T, fr *FrameReader) (Table, string) {
	t.Helper()
	tbl, err := fr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	data, err := io.ReadAll(tbl.Data)
	if err != nil {
		t.Fatalf("read %s: %v", tbl.Name, err)
	}
	return tbl, string(data)
}

func TestFramesRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	writeSampleFrames(t, &buf)
	fr := NewFrameReader(&buf)

	tbl, data := readTable(t, fr)
	if tbl.Name != "empty_table" || !reflect.DeepEqual(tbl.Columns, []string{"id"}) || data != "" {
		t.Errorf("first table = %q %v %q", tbl.Name, tbl.Columns, data)
	}
	tbl, data = readTable(t, fr)
	if tbl.Name != "users" || !reflect.DeepEqual(tbl.Columns, []string{"id", "name", "note"}) {
		t.Errorf("second table = %q %v", tbl.Name, tbl.Columns)
	}
	if data != copyText {
		t.Errorf("second table data = %q, want %q", data, copyText)
	}
	if _, err := fr.Next(); err != io.EOF {
		t.Fatalf("Next after the last table = %v, want io.EOF", err)
	}
	if got, want := fr.Manifest(), sampleManifest(); !manifestEqual(got, want) {
		t.Errorf("Manifest = %+v, want %+v", got, want)
	}
}

func manifestEqual(a, b Manifest) bool {
	if !a.Header.CreatedAt.Equal(b.Header.CreatedAt) {
		return false
	}
	a.Header.CreatedAt, b.Header.CreatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

func TestFramesSkipUnreadTableData(t *testing.T) {
	var buf bytes.Buffer
	writeSampleFrames(t, &buf)
	fr := NewFrameReader(&buf)

	if _, err := fr.Next(); err != nil { // empty_table, left unread
		t.Fatal(err)
	}
	if _, err := fr.Next(); err != nil { // users, left unread
		t.Fatal(err)
	}
	if _, err := fr.Next(); err != io.EOF {
		t.Fatalf("Next = %v, want io.EOF", err)
	}
	if got := fr.Manifest(); len(got.Tables) != 2 {
		t.Errorf("Manifest = %+v", got)
	}
}

func TestFramesChunkLargeWrites(t *testing.T) {
	big := make([]byte, 2<<20+1234)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	fw := NewFrameWriter(&buf)
	if err := fw.Begin("big", []string{"c"}); err != nil {
		t.Fatal(err)
	}
	if n, err := fw.Write(big); err != nil || n != len(big) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if err := fw.End(1); err != nil {
		t.Fatal(err)
	}
	if err := fw.Manifest(sampleManifest()); err != nil {
		t.Fatal(err)
	}

	// Walk the raw frames: no data chunk may exceed 1 MiB.
	raw := buf.Bytes()
	var chunks int
	for len(raw) > 0 {
		kind, n := raw[0], binary.BigEndian.Uint32(raw[1:5])
		if kind == 'D' {
			chunks++
			if n > 1<<20 {
				t.Errorf("data chunk of %d bytes exceeds 1 MiB", n)
			}
		}
		raw = raw[5+n:]
	}
	if chunks < 3 {
		t.Errorf("%d data chunks for %d bytes, want at least 3", chunks, len(big))
	}

	fr := NewFrameReader(&buf)
	_, data := readTable(t, fr)
	if data != string(big) {
		t.Error("large table did not round-trip")
	}
}

func TestFramesWithoutManifestAreCorrupt(t *testing.T) {
	var buf bytes.Buffer
	fw := NewFrameWriter(&buf)
	if err := fw.Begin("users", []string{"id"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("1\n")); err != nil {
		t.Fatal(err)
	}
	if err := fw.End(1); err != nil {
		t.Fatal(err)
	}

	fr := NewFrameReader(&buf)
	readTable(t, fr)
	if _, err := fr.Next(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Next without a manifest = %v, want ErrCorrupt", err)
	}
}

func TestFramesTruncatedMidFrameAreCorrupt(t *testing.T) {
	var full bytes.Buffer
	writeSampleFrames(t, &full)

	for _, cut := range []int{3, 8, full.Len() - 3} {
		fr := NewFrameReader(bytes.NewReader(full.Bytes()[:cut]))
		var err error
		for err == nil {
			var tbl Table
			if tbl, err = fr.Next(); err == nil {
				_, err = io.Copy(io.Discard, tbl.Data)
			}
		}
		if !errors.Is(err, ErrCorrupt) {
			t.Errorf("cut at %d: err = %v, want ErrCorrupt", cut, err)
		}
	}
}

func rawFrame(kind byte, payload string) []byte {
	b := []byte{kind, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[1:], uint32(len(payload)))
	return append(b, payload...)
}

func TestFrameReaderRejectsBadStreams(t *testing.T) {
	manifest := string(rawFrame('M', `{"header":{"format_version":1},"tables":[],"sequences":[]}`))
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	tests := []struct {
		name string
		in   []byte
	}{
		{"unknown frame type", rawFrame('X', "")},
		{"data outside a table", cat(rawFrame('D', "1\n"))},
		{"table end outside a table", cat(rawFrame('E', `{"rows":0}`))},
		{"table begin inside a table", cat(rawFrame('T', `{"name":"a"}`), rawFrame('T', `{"name":"b"}`), rawFrame('E', `{"rows":0}`))},
		{"manifest inside a table", cat(rawFrame('T', `{"name":"a"}`), []byte(manifest))},
		{"table begin is not json", rawFrame('T', "nope")},
		{"manifest is not json", rawFrame('M', "nope")},
		{"oversized frame", []byte{'M', 0xff, 0xff, 0xff, 0xff}},
		{"bytes after the manifest", cat([]byte(manifest), []byte("extra"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fr := NewFrameReader(bytes.NewReader(tc.in))
			var err error
			for err == nil {
				var tbl Table
				if tbl, err = fr.Next(); err == nil {
					_, err = io.Copy(io.Discard, tbl.Data)
				}
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestFrameWriterRejectsMisuse(t *testing.T) {
	tests := []struct {
		name string
		do   func(fw *FrameWriter) error
	}{
		{"write before begin", func(fw *FrameWriter) error { _, err := fw.Write([]byte("x")); return err }},
		{"end before begin", func(fw *FrameWriter) error { return fw.End(0) }},
		{"begin twice", func(fw *FrameWriter) error {
			_ = fw.Begin("a", nil)
			return fw.Begin("b", nil)
		}},
		{"manifest inside a table", func(fw *FrameWriter) error {
			_ = fw.Begin("a", nil)
			return fw.Manifest(Manifest{})
		}},
		{"begin after manifest", func(fw *FrameWriter) error {
			_ = fw.Manifest(Manifest{})
			return fw.Begin("a", nil)
		}},
		{"second manifest", func(fw *FrameWriter) error {
			_ = fw.Manifest(Manifest{})
			return fw.Manifest(Manifest{})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.do(NewFrameWriter(io.Discard)); err == nil {
				t.Fatal("misuse was accepted")
			}
		})
	}
}

func TestFramesThroughEncryption(t *testing.T) {
	var file bytes.Buffer
	if err := WriteHeader(&file, sampleHeader()); err != nil {
		t.Fatal(err)
	}
	enc, err := Encrypt(&file, keyOne)
	if err != nil {
		t.Fatal(err)
	}
	writeSampleFrames(t, enc)
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}

	read := func(t *testing.T, file []byte, key [32]byte) error {
		t.Helper()
		src := bytes.NewReader(file)
		if _, err := ReadHeader(src); err != nil {
			t.Fatalf("ReadHeader: %v", err)
		}
		dec, err := Decrypt(src, key)
		if err != nil {
			return err
		}
		fr := NewFrameReader(dec)
		for {
			tbl, err := fr.Next()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			if _, err := io.Copy(io.Discard, tbl.Data); err != nil {
				return err
			}
		}
	}

	t.Run("intact", func(t *testing.T) {
		if err := read(t, file.Bytes(), keyOne); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("wrong key", func(t *testing.T) {
		if err := read(t, file.Bytes(), keyTwo); !errors.Is(err, ErrWrongKey) {
			t.Fatalf("err = %v, want ErrWrongKey", err)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		if err := read(t, file.Bytes()[:file.Len()-20], keyOne); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("err = %v, want ErrCorrupt", err)
		}
	})
	t.Run("tampered", func(t *testing.T) {
		b := bytes.Clone(file.Bytes())
		b[len(b)-40] ^= 0x01
		if err := read(t, b, keyOne); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("err = %v, want ErrCorrupt", err)
		}
	})
}
