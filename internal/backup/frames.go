package backup

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Frame types. A frame is one type byte, a big-endian uint32 payload length
// and the payload: JSON for T, E and M, raw table bytes for D.
const (
	frameTableBegin = 'T'
	frameData       = 'D'
	frameTableEnd   = 'E'
	frameManifest   = 'M'
)

const (
	frameHeaderLen = 5
	// maxDataChunk bounds a D frame; FrameWriter splits larger writes.
	maxDataChunk = 1 << 20
	// maxJSONFrame bounds a T, E or M frame, so a damaged length cannot make
	// the reader allocate gigabytes.
	maxJSONFrame = 16 << 20
)

type tableBeginFrame struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

type tableEndFrame struct {
	Rows int64 `json:"rows"`
}

// FrameWriter writes the frame stream that sits inside the encrypted
// payload: per table a Begin, any number of Write calls and an End, then one
// Manifest as the last frame. After the first error every call fails.
type FrameWriter struct {
	w         io.Writer
	err       error
	inTable   bool
	finalized bool
}

// NewFrameWriter returns a FrameWriter that writes frames to w.
func NewFrameWriter(w io.Writer) *FrameWriter { return &FrameWriter{w: w} }

// Begin starts a table.
func (f *FrameWriter) Begin(name string, columns []string) error {
	if err := f.check(false); err != nil {
		return err
	}
	f.inTable = true
	return f.writeJSON(frameTableBegin, tableBeginFrame{Name: name, Columns: columns})
}

// Write appends p, one table's COPY text, to the open table. It splits p into
// frames of at most 1 MiB.
func (f *FrameWriter) Write(p []byte) (int, error) {
	if err := f.check(true); err != nil {
		return 0, err
	}
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maxDataChunk {
			chunk = chunk[:maxDataChunk]
		}
		if err := f.writeFrame(frameData, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

// End closes the open table and records its row count.
func (f *FrameWriter) End(rows int64) error {
	if err := f.check(true); err != nil {
		return err
	}
	f.inTable = false
	return f.writeJSON(frameTableEnd, tableEndFrame{Rows: rows})
}

// Manifest writes the last frame. Nothing may follow it.
func (f *FrameWriter) Manifest(m Manifest) error {
	if err := f.check(false); err != nil {
		return err
	}
	f.finalized = true
	return f.writeJSON(frameManifest, m)
}

// check reports a sticky error or a call made in the wrong state; wantTable
// is whether the call needs an open table.
func (f *FrameWriter) check(wantTable bool) error {
	switch {
	case f.err != nil:
		return f.err
	case f.finalized:
		return errors.New("backup frames: write after the manifest")
	case wantTable && !f.inTable:
		return errors.New("backup frames: no table is open")
	case !wantTable && f.inTable:
		return errors.New("backup frames: a table is still open")
	}
	return nil
}

func (f *FrameWriter) writeJSON(kind byte, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		f.err = fmt.Errorf("encode backup frame: %w", err)
		return f.err
	}
	return f.writeFrame(kind, payload)
}

func (f *FrameWriter) writeFrame(kind byte, payload []byte) error {
	frame := make([]byte, frameHeaderLen, frameHeaderLen+len(payload))
	frame[0] = kind
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	frame = append(frame, payload...)
	if _, err := f.w.Write(frame); err != nil {
		f.err = fmt.Errorf("write backup frame: %w", err)
		return f.err
	}
	return nil
}

// Table is one table of the stream. Data yields the COPY text and is valid
// until the next call to FrameReader.Next.
type Table struct {
	Name    string
	Columns []string
	Data    io.Reader
}

// FrameReader reads the stream a FrameWriter wrote. Anything that is not a
// well-formed stream ending in a manifest fails with ErrCorrupt.
type FrameReader struct {
	r        io.Reader
	err      error // sticky
	manifest Manifest

	inTable    bool
	remaining  uint32 // bytes left in the current D frame
	generation int    // identifies the current table for its Data reader
}

// NewFrameReader returns a FrameReader that reads frames from r.
func NewFrameReader(r io.Reader) *FrameReader { return &FrameReader{r: r} }

// Manifest returns the manifest once Next has returned io.EOF.
func (f *FrameReader) Manifest() Manifest { return f.manifest }

// Next returns the next table, skipping whatever is left of the previous one.
// It returns io.EOF after the manifest, and only once the stream behind it
// has ended too, so the decryption layer has authenticated everything.
func (f *FrameReader) Next() (Table, error) {
	if f.err != nil {
		return Table{}, f.err
	}
	if f.inTable {
		if _, err := io.Copy(io.Discard, &tableData{f: f, generation: f.generation}); err != nil {
			return Table{}, err
		}
	}

	kind, n, err := f.readFrameHeader()
	if err != nil {
		return Table{}, f.fail(err)
	}
	switch kind {
	case frameTableBegin:
		var begin tableBeginFrame
		if err := f.readJSON(n, &begin); err != nil {
			return Table{}, f.fail(err)
		}
		f.inTable = true
		f.remaining = 0
		f.generation++
		return Table{
			Name:    begin.Name,
			Columns: begin.Columns,
			Data:    &tableData{f: f, generation: f.generation},
		}, nil
	case frameManifest:
		if err := f.readJSON(n, &f.manifest); err != nil {
			return Table{}, f.fail(err)
		}
		return Table{}, f.fail(f.expectEnd())
	default:
		return Table{}, f.fail(fmt.Errorf("%w: unexpected frame %q between tables", ErrCorrupt, kind))
	}
}

// fail records err as the sticky result; a nil err becomes io.EOF.
func (f *FrameReader) fail(err error) error {
	if err == nil {
		err = io.EOF
	}
	f.err = err
	return err
}

// readFrameHeader reads a frame's type and length. A stream that ends before
// the next frame is a stream without its manifest.
func (f *FrameReader) readFrameHeader() (byte, uint32, error) {
	var hdr [frameHeaderLen]byte
	if _, err := io.ReadFull(f.r, hdr[:]); err != nil {
		return 0, 0, f.corruptOnEOF(err, "stream ends before the manifest")
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	limit := uint32(maxJSONFrame)
	if hdr[0] == frameData {
		limit = maxDataChunk
	}
	if n > limit {
		return 0, 0, fmt.Errorf("%w: frame %q of %d bytes exceeds its %d byte limit", ErrCorrupt, hdr[0], n, limit)
	}
	return hdr[0], n, nil
}

func (f *FrameReader) readJSON(n uint32, v any) error {
	payload := make([]byte, n)
	if _, err := io.ReadFull(f.r, payload); err != nil {
		return f.corruptOnEOF(err, "stream ends inside a frame")
	}
	if err := json.Unmarshal(payload, v); err != nil {
		return fmt.Errorf("%w: decode frame: %v", ErrCorrupt, err)
	}
	return nil
}

// expectEnd requires the stream to end right after the manifest. Reading to
// the end is what makes the decryption layer check its final authentication
// tag and compression checksum.
func (f *FrameReader) expectEnd() error {
	var one [1]byte
	n, err := f.r.Read(one[:])
	for n == 0 && err == nil {
		n, err = f.r.Read(one[:])
	}
	switch {
	case n > 0:
		return fmt.Errorf("%w: data after the manifest", ErrCorrupt)
	case errors.Is(err, io.EOF):
		return nil
	default:
		return f.corruptOnEOF(err, "stream ends badly after the manifest")
	}
}

// corruptOnEOF turns a short read into ErrCorrupt and leaves other errors,
// such as a failing source or an already classified ErrCorrupt, alone.
func (f *FrameReader) corruptOnEOF(err error, what string) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: %s", ErrCorrupt, what)
	}
	return err
}

// tableData streams the D frames of one table up to its E frame.
type tableData struct {
	f          *FrameReader
	generation int
}

func (t *tableData) Read(p []byte) (int, error) {
	f := t.f
	if t.generation != f.generation {
		return 0, errors.New("backup frames: table data read after Next")
	}
	if f.err != nil {
		return 0, f.err
	}
	if !f.inTable {
		return 0, io.EOF
	}
	for f.remaining == 0 {
		kind, n, err := f.readFrameHeader()
		if err != nil {
			return 0, f.fail(err)
		}
		switch kind {
		case frameData:
			f.remaining = n
		case frameTableEnd:
			var end tableEndFrame
			if err := f.readJSON(n, &end); err != nil {
				return 0, f.fail(err)
			}
			f.inTable = false
			return 0, io.EOF
		default:
			return 0, f.fail(fmt.Errorf("%w: unexpected frame %q inside a table", ErrCorrupt, kind))
		}
	}
	if uint32(len(p)) > f.remaining {
		p = p[:f.remaining]
	}
	n, err := f.r.Read(p)
	f.remaining -= uint32(n)
	if err != nil {
		if errors.Is(err, io.EOF) {
			if f.remaining > 0 {
				return n, f.fail(f.corruptOnEOF(err, "stream ends inside a data frame"))
			}
			err = nil
		} else {
			return n, f.fail(f.corruptOnEOF(err, "stream ends inside a data frame"))
		}
	}
	return n, err
}
