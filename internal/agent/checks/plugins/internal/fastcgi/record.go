package fastcgi

import (
	"encoding/binary"
	"errors"
	"io"
)

// FastCGI 1.0 constants the client uses.
const (
	version1         = 1
	typeBeginRequest = 1
	typeEndRequest   = 3
	typeParams       = 4
	typeStdin        = 5
	typeStdout       = 6
	typeStderr       = 7
	roleResponder    = 1
	requestComplete  = 0
	headerLen        = 8
	maxContent       = 0xffff
	// requestID is the one request a connection carries.
	requestID = 1
)

// appendRecord appends one record with no padding (FastCGI allows none).
func appendRecord(b []byte, typ byte, content []byte) []byte {
	b = append(b, version1, typ, 0, requestID, 0, 0, 0, 0)
	binary.BigEndian.PutUint16(b[len(b)-4:], uint16(len(content))) //nolint:gosec // callers keep content within maxContent
	return append(b, content...)
}

// appendParam appends one name-value pair: a length below 128 is one
// byte, any other four with the high bit set.
func appendParam(b []byte, name, value string) []byte {
	for _, n := range []int{len(name), len(value)} {
		if n < 0x80 {
			b = append(b, byte(n))
			continue
		}
		b = binary.BigEndian.AppendUint32(b, uint32(n)|0x80000000) //nolint:gosec // values are bounded far below 2^31
	}
	return append(append(b, name...), value...)
}

// record is one decoded record header.
type record struct {
	typ        byte
	id         uint16
	contentLen int
	paddingLen int
}

var errVersion = errors.New("fastcgi: version")

// readHeader reads a record header. A stream that ends inside it, or
// before it once the request is underway, is io.ErrUnexpectedEOF.
func readHeader(r io.Reader) (record, error) {
	var h [headerLen]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return record{}, err
	}
	if h[0] != version1 {
		return record{}, errVersion
	}
	return record{
		typ:        h[1],
		id:         binary.BigEndian.Uint16(h[2:4]),
		contentLen: int(binary.BigEndian.Uint16(h[4:6])),
		paddingLen: int(h[6]),
	}, nil
}
