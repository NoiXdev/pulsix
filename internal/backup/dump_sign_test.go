package backup_test

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/backup"
)

func TestDumpSignsHeaderAndManifest(t *testing.T) {
	src := sourceDB(t)
	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	data, h := dumpBytes(t, src.Pool(), backup.DumpOptions{Kind: backup.KindScheduled, Now: func() time.Time { return at }})

	plain, err := backup.ReadHeader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !plain.Authentic(backup.HeaderKey(testKey)) {
		t.Fatalf("the plaintext header %+v is not authentic under the master key's header key", plain)
	}
	if plain.Authentic(backup.HeaderKey([32]byte{9})) {
		t.Fatal("the plaintext header is authentic under another master key")
	}
	if h.MAC != plain.MAC {
		t.Errorf("Dump returned MAC %q, the file holds %q", h.MAC, plain.MAC)
	}

	m, err := backup.Verify(bytes.NewReader(data), testKey)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if m.Header.MAC != plain.MAC {
		t.Errorf("manifest MAC %q, plaintext MAC %q", m.Header.MAC, plain.MAC)
	}

	// The manifest's copy carries the MAC too: stripping it from the
	// plaintext header alone makes the file corrupt.
	stripped := retitle(t, data, func(x *backup.Header) { x.MAC = "" })
	if _, err := backup.Verify(bytes.NewReader(stripped), testKey); !errors.Is(err, backup.ErrCorrupt) {
		t.Fatalf("Verify of a file whose plaintext MAC was stripped: %v, want ErrCorrupt", err)
	}

	objs := []backup.Object{{Name: backup.ObjectName(at, backup.KindScheduled), Size: int64(len(data)), Header: plain}}
	backup.MarkVerified(objs, backup.HeaderKey(testKey))
	if !objs[0].Verified {
		t.Fatal("a dump stored under its own name is not verified")
	}
}

// unsign rewrites a backup file the way a build before signed headers wrote
// it: no MAC in the plaintext header nor in the manifest's copy.
func unsign(t *testing.T, data []byte) []byte {
	t.Helper()
	r := bytes.NewReader(data)
	h, err := backup.ReadHeader(r)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := backup.Decrypt(r, testKey)
	if err != nil {
		t.Fatal(err)
	}
	h.MAC = ""
	var out bytes.Buffer
	if err := backup.WriteHeader(&out, h); err != nil {
		t.Fatal(err)
	}
	enc, err := backup.Encrypt(&out, testKey)
	if err != nil {
		t.Fatal(err)
	}
	fr, fw := backup.NewFrameReader(dec), backup.NewFrameWriter(enc)
	for {
		tb, err := fr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := fw.Begin(tb.Name, tb.Columns); err != nil {
			t.Fatal(err)
		}
		rows, err := io.ReadAll(tb.Data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(rows); err != nil {
			t.Fatal(err)
		}
		if err := fw.End(int64(bytes.Count(rows, []byte{'\n'}))); err != nil {
			t.Fatal(err)
		}
	}
	m := fr.Manifest()
	m.Header.MAC = ""
	if err := fw.Manifest(m); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestRestoreAcceptsUnsignedBackup covers Review Focus 1: a backup written
// before headers were signed still restores.
func TestRestoreAcceptsUnsignedBackup(t *testing.T) {
	src := sourceDB(t)
	fillEveryTable(t, src.Pool())
	signed, _ := dumpBytes(t, src.Pool(), backup.DumpOptions{IncludeMetrics: true})
	data := unsign(t, signed)
	plain, err := backup.ReadHeader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if plain.MAC != "" {
		t.Fatalf("unsign left MAC %q", plain.MAC)
	}

	target := targetDB(t)
	m, err := restoreBytes(target, testKey, data, backup.RestoreOptions{BinarySchemaVersion: schemaVersion(t, src)})
	if err != nil {
		t.Fatalf("restore of an unsigned backup: %v", err)
	}
	if m.Header.MAC != "" {
		t.Errorf("manifest MAC = %q, want none", m.Header.MAC)
	}
	for _, name := range publicTables(t, src.Pool()) {
		if got, want := tableDigest(t, target.Pool(), name), tableDigest(t, src.Pool(), name); got != want {
			t.Errorf("%s: restored rows differ from the source", name)
		}
	}
}
