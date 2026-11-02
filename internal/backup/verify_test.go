package backup_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/backup"
)

// craftedTable is one table of a hand-built backup stream.
type craftedTable struct {
	name string
	data string
	rows int64 // what the E frame says
}

// craft builds a backup file by hand, so Verify can be fed streams Dump never
// writes: plain is the plaintext header, m the manifest.
func craft(t *testing.T, plain backup.Header, tables []craftedTable, m backup.Manifest) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := backup.WriteHeader(&buf, plain); err != nil {
		t.Fatal(err)
	}
	enc, err := backup.Encrypt(&buf, testKey)
	if err != nil {
		t.Fatal(err)
	}
	fw := backup.NewFrameWriter(enc)
	for _, tb := range tables {
		if err := fw.Begin(tb.name, []string{"a", "b"}); err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(tb.data)); err != nil {
			t.Fatal(err)
		}
		if err := fw.End(tb.rows); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Manifest(m); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func craftedHeader() backup.Header {
	return backup.Header{
		FormatVersion:   backup.FormatVersion,
		CreatedAt:       time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC),
		Kind:            "scheduled",
		PulsixVersion:   "1.2.3",
		SchemaVersion:   74,
		IncludesMetrics: true,
	}
}

// retitle replaces the plaintext header of a backup file and leaves the
// encrypted payload as it is.
func retitle(t *testing.T, data []byte, edit func(*backup.Header)) []byte {
	t.Helper()
	r := bytes.NewReader(data)
	h, err := backup.ReadHeader(r)
	if err != nil {
		t.Fatal(err)
	}
	payload := data[len(data)-r.Len():]
	edit(&h)
	var buf bytes.Buffer
	if err := backup.WriteHeader(&buf, h); err != nil {
		t.Fatal(err)
	}
	buf.Write(payload)
	return buf.Bytes()
}

func TestVerifyAcceptsAnIntactFile(t *testing.T) {
	h := craftedHeader()
	m := backup.Manifest{
		Header:    h,
		Tables:    []backup.TableCount{{Name: "a", Rows: 2}, {Name: "empty", Rows: 0}},
		Sequences: []backup.SequenceValue{{Name: "s", LastValue: 5, IsCalled: true}},
	}
	data := craft(t, h, []craftedTable{{"a", "1\tx\n2\t\\N\n", 2}, {"empty", "", 0}}, m)
	got, err := backup.Verify(bytes.NewReader(data), testKey)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(m)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("Verify returned %s, want %s", gotJSON, wantJSON)
	}
}

func TestVerifyRejects(t *testing.T) {
	h := craftedHeader()
	manifest := func(tables ...backup.TableCount) backup.Manifest {
		return backup.Manifest{Header: h, Tables: tables}
	}
	intact := craft(t, h, []craftedTable{{"a", "1\tx\n", 1}}, manifest(backup.TableCount{Name: "a", Rows: 1}))

	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"plaintext header differs from the manifest's",
			retitle(t, intact, func(x *backup.Header) { x.SchemaVersion = 75 }), backup.ErrCorrupt},
		{"plaintext creation time differs",
			retitle(t, intact, func(x *backup.Header) { x.CreatedAt = x.CreatedAt.Add(time.Second) }), backup.ErrCorrupt},
		{"plaintext metrics flag differs",
			retitle(t, intact, func(x *backup.Header) { x.IncludesMetrics = false }), backup.ErrCorrupt},
		{"manifest row count differs from the data",
			craft(t, h, []craftedTable{{"a", "1\tx\n2\ty\n", 2}}, manifest(backup.TableCount{Name: "a", Rows: 3})), backup.ErrCorrupt},
		{"E frame and manifest agree but the data has fewer rows",
			craft(t, h, []craftedTable{{"a", "1\tx\n", 2}}, manifest(backup.TableCount{Name: "a", Rows: 2})), backup.ErrCorrupt},
		{"data does not end with a newline",
			craft(t, h, []craftedTable{{"a", "1\tx\n2\ty", 2}}, manifest(backup.TableCount{Name: "a", Rows: 2})), backup.ErrCorrupt},
		{"manifest lists a table the stream lacks",
			craft(t, h, []craftedTable{{"a", "1\tx\n", 1}}, manifest(backup.TableCount{Name: "a", Rows: 1}, backup.TableCount{Name: "b", Rows: 0})), backup.ErrCorrupt},
		{"stream has a table the manifest lacks",
			craft(t, h, []craftedTable{{"a", "1\tx\n", 1}, {"b", "", 0}}, manifest(backup.TableCount{Name: "a", Rows: 1})), backup.ErrCorrupt},
		{"a table appears twice",
			craft(t, h, []craftedTable{{"a", "1\tx\n", 1}, {"a", "1\tx\n", 1}}, manifest(backup.TableCount{Name: "a", Rows: 1}, backup.TableCount{Name: "a", Rows: 1})), backup.ErrCorrupt},
		{"truncated", intact[:len(intact)-20], backup.ErrCorrupt},
		{"not a backup", []byte("hello world, this is not a backup file\n"), backup.ErrNotABackup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := backup.Verify(bytes.NewReader(tc.data), testKey); !errors.Is(err, tc.want) {
				t.Errorf("Verify: %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("wrong key", func(t *testing.T) {
		if _, err := backup.Verify(bytes.NewReader(intact), [32]byte{9}); !errors.Is(err, backup.ErrWrongKey) {
			t.Errorf("Verify: %v, want ErrWrongKey", err)
		}
	})
}
