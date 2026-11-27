// Package fastcgitest is an in-process FastCGI responder for tests: it
// decodes one request per connection with its own record code (not the
// client's) and lets the test write whatever bytes it wants back. It is
// imported from _test.go files only.
package fastcgitest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Record types (FastCGI 1.0).
const (
	TypeBeginRequest = 1
	TypeEndRequest   = 3
	TypeParams       = 4
	TypeStdin        = 5
	TypeStdout       = 6
	TypeStderr       = 7
	TypeUnknownType  = 11
)

// Request is what the server decoded from one connection.
type Request struct {
	ID       uint16
	Role     uint16
	KeepConn bool
	Params   map[string]string
	// ParamOrder is the params' names in the order sent.
	ParamOrder []string
	Stdin      []byte
}

// Handler answers one request. It writes the raw response to conn and
// returns; the server closes conn afterwards.
type Handler func(req Request, conn net.Conn)

// Server is a FastCGI responder on a TCP port or a unix socket.
type Server struct {
	// Network and Address are what a client dials.
	Network, Address string
	ln               net.Listener
	mu               sync.Mutex
	handler          Handler
	reqs             []Request
	wg               sync.WaitGroup
	done             chan struct{}
}

// NewTCP starts a server on 127.0.0.1; it closes with t.
func NewTCP(t *testing.T, h Handler) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return start(t, "tcp", ln.Addr().String(), ln, h)
}

// NewUnix starts a server on a unix socket in a short temporary directory
// (t.TempDir can exceed the socket path limit on macOS); it closes with t.
func NewUnix(t *testing.T, h Handler) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "fcgi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "fpm.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	return start(t, "unix", path, ln, h)
}

func start(t *testing.T, network, addr string, ln net.Listener, h Handler) *Server {
	s := &Server{Network: network, Address: addr, ln: ln, handler: h, done: make(chan struct{})}
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(s.Close)
	return s
}

// Close stops the server and waits for its handlers; a handler that
// blocks must return once Done is closed.
func (s *Server) Close() {
	select {
	case <-s.done:
		return
	default:
	}
	close(s.done)
	_ = s.ln.Close()
	s.wg.Wait()
}

// Done is closed when the server closes.
func (s *Server) Done() <-chan struct{} { return s.done }

// SetHandler changes how the next requests are answered.
func (s *Server) SetHandler(h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

// Requests is every request decoded so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { _ = conn.Close() }()
			req, err := ReadRequest(conn)
			if err != nil {
				return
			}
			s.mu.Lock()
			s.reqs = append(s.reqs, req)
			h := s.handler
			s.mu.Unlock()
			h(req, conn)
		}()
	}
}

// ReadRequest decodes BEGIN_REQUEST, PARAMS up to the empty one and STDIN
// up to the empty one.
func ReadRequest(r io.Reader) (Request, error) {
	req := Request{Params: map[string]string{}}
	var params []byte
	paramsDone, began := false, false
	for {
		typ, id, content, err := readRecord(r)
		if err != nil {
			return req, err
		}
		switch typ {
		case TypeBeginRequest:
			if len(content) != 8 {
				return req, errors.New("begin request body")
			}
			req.ID = id
			req.Role = binary.BigEndian.Uint16(content)
			req.KeepConn = content[2]&1 != 0
			began = true
		case TypeParams:
			if len(content) == 0 {
				paramsDone = true
				if err := decodeParams(params, &req); err != nil {
					return req, err
				}
				continue
			}
			params = append(params, content...)
		case TypeStdin:
			if len(content) == 0 {
				if !began || !paramsDone {
					return req, errors.New("stdin before params")
				}
				return req, nil
			}
			req.Stdin = append(req.Stdin, content...)
		default:
			return req, fmt.Errorf("unexpected record type %d", typ)
		}
	}
}

func readRecord(r io.Reader) (typ byte, id uint16, content []byte, err error) {
	var h [8]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return 0, 0, nil, err
	}
	if h[0] != 1 {
		return 0, 0, nil, errors.New("version")
	}
	content = make([]byte, int(binary.BigEndian.Uint16(h[4:6]))+int(h[6]))
	if _, err = io.ReadFull(r, content); err != nil {
		return 0, 0, nil, err
	}
	return h[1], binary.BigEndian.Uint16(h[2:4]), content[:binary.BigEndian.Uint16(h[4:6])], nil
}

func decodeParams(b []byte, req *Request) error {
	readLen := func() (int, bool) {
		if len(b) == 0 {
			return 0, false
		}
		if b[0]&0x80 == 0 {
			n := int(b[0])
			b = b[1:]
			return n, true
		}
		if len(b) < 4 {
			return 0, false
		}
		n := int(binary.BigEndian.Uint32(b) & 0x7fffffff)
		b = b[4:]
		return n, true
	}
	for len(b) > 0 {
		nl, ok1 := readLen()
		vl, ok2 := readLen()
		if !ok1 || !ok2 || nl+vl > len(b) {
			return errors.New("params")
		}
		name, value := string(b[:nl]), string(b[nl:nl+vl])
		b = b[nl+vl:]
		req.Params[name] = value
		req.ParamOrder = append(req.ParamOrder, name)
	}
	return nil
}

// Record encodes one record with padding bytes of value 0xEE (a client
// must skip them, not read them).
func Record(typ byte, id uint16, content []byte, padding int) []byte {
	var b bytes.Buffer
	b.Write([]byte{1, typ, byte(id >> 8), byte(id), byte(len(content) >> 8), byte(len(content)), byte(padding), 0})
	b.Write(content)
	b.Write(bytes.Repeat([]byte{0xEE}, padding))
	return b.Bytes()
}

// End is an END_REQUEST record with the protocol status.
func End(id uint16, protocolStatus byte) []byte {
	return Record(TypeEndRequest, id, []byte{0, 0, 0, 0, protocolStatus, 0, 0, 0}, 0)
}

// Stdout splits s into STDOUT records of at most chunk bytes each, padded
// to a multiple of 8, followed by the empty STDOUT record.
func Stdout(id uint16, s string, chunk int) []byte {
	var b bytes.Buffer
	for len(s) > 0 {
		n := min(chunk, len(s), 65535)
		b.Write(Record(TypeStdout, id, []byte(s[:n]), (8-n%8)%8))
		s = s[n:]
	}
	b.Write(Record(TypeStdout, id, nil, 0))
	return b.Bytes()
}

// CGI is a CGI response: the Status header (none when status is 0), a
// Content-type header and the body.
func CGI(status int, body string) string {
	var h strings.Builder
	if status != 0 {
		fmt.Fprintf(&h, "Status: %d %s\r\n", status, "Reason")
	}
	h.WriteString("Content-type: application/json\r\n\r\n")
	return h.String() + body
}

// Respond answers like FPM: the CGI response on STDOUT in records of 1000
// bytes, then END_REQUEST complete.
func Respond(status int, body string) Handler {
	return func(req Request, conn net.Conn) {
		_, _ = conn.Write(append(Stdout(req.ID, CGI(status, body), 1000), End(req.ID, 0)...))
	}
}

// Raw answers with exactly b.
func Raw(b []byte) Handler {
	return func(_ Request, conn net.Conn) { _, _ = conn.Write(b) }
}
