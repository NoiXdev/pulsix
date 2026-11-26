// Package fastcgi is a minimal FastCGI client (backlog 3.11a): one GET-like
// responder request per connection, as a web server would send to PHP-FPM,
// and the CGI response it gets back. It exists so the phpfpm plugin can
// read a pool's status page from the pool's own listen address with no web
// server in between. Everything is bounded: the bytes read (per stream and
// in all), the time (the caller's context), the params sent (a fixed set
// whose only variable values are the path and the query). It is internal
// to plugins/.
package fastcgi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	// maxHeader bounds the CGI header block before the body.
	maxHeader = 16 << 10
	// maxStderr bounds what the server may write to STDERR (FPM logs a
	// script's warnings there); it is read and dropped.
	maxStderr = 64 << 10
	// rawSlack is what the raw byte bound allows beyond twice the body
	// bound: headers, STDERR and record framing.
	rawSlack = 128 << 10
)

// The client's own failures; their texts are fixed and safe to show.
var (
	// ErrTooLarge: the body, the header block, STDERR or the raw stream
	// passed its bound.
	ErrTooLarge = errors.New("response too large")
	// ErrProtocol: the answer is not a well-formed FastCGI responder answer
	// with a CGI header block.
	ErrProtocol = errors.New("unexpected response format")
	// ErrRejected: the server ended the request with a protocol status
	// other than REQUEST_COMPLETE (overloaded, unknown role, no
	// multiplexing).
	ErrRejected = errors.New("request rejected")
)

// Response is the CGI response: the Status header's code (200 when the
// header is absent) and the body.
type Response struct {
	Status int
	Body   []byte
}

// Get sends one responder request for path with query to the FastCGI
// server at network ("tcp" or "unix") and address, and returns its
// response, the body at most maxBody bytes. The caller's context bounds the
// dial and the exchange; once it ends, Get returns its error. Dial and I/O
// errors are returned as they are and may name the address: a caller that
// shows them must classify them into fixed texts first.
func Get(ctx context.Context, network, address, path, query string, maxBody int) (Response, error) {
	req, err := buildRequest(path, query)
	if err != nil {
		return Response{}, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, address)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	// A context that ends (cancelled, or its deadline) unblocks any read or
	// write in progress.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	resp, err := exchange(conn, req, maxBody)
	if err != nil && ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	return resp, err
}

// buildRequest is BEGIN_REQUEST (responder, no keep-conn), the fixed
// params, the empty PARAMS record and the empty STDIN record.
func buildRequest(path, query string) ([]byte, error) {
	var params []byte
	for _, p := range [][2]string{
		{"GATEWAY_INTERFACE", "CGI/1.1"},
		{"REQUEST_METHOD", "GET"},
		{"SCRIPT_FILENAME", path},
		{"SCRIPT_NAME", path},
		{"DOCUMENT_URI", path},
		{"REQUEST_URI", path + "?" + query},
		{"QUERY_STRING", query},
		{"SERVER_PROTOCOL", "HTTP/1.1"},
	} {
		params = appendParam(params, p[0], p[1])
	}
	if len(params) > maxContent {
		return nil, errors.New("fastcgi: request too large")
	}
	b := appendRecord(nil, typeBeginRequest, []byte{0, roleResponder, 0, 0, 0, 0, 0, 0})
	b = appendRecord(b, typeParams, params)
	b = appendRecord(b, typeParams, nil)
	return appendRecord(b, typeStdin, nil), nil
}

// exchange writes the request and reads records up to END_REQUEST.
func exchange(conn net.Conn, req []byte, maxBody int) (Response, error) {
	if _, err := conn.Write(req); err != nil {
		return Response{}, err
	}
	r := bufio.NewReader(&limitReader{r: conn, n: 2*int64(maxBody) + rawSlack})
	var stdout []byte
	stderr := 0
	buf := make([]byte, maxContent+0xff)
	for {
		h, err := readHeader(r)
		if errors.Is(err, errVersion) {
			return Response{}, ErrProtocol
		}
		if err != nil {
			return Response{}, err
		}
		content := buf[:h.contentLen+h.paddingLen]
		if _, err := io.ReadFull(r, content); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return Response{}, err
		}
		content = content[:h.contentLen]
		if h.id != requestID {
			return Response{}, ErrProtocol
		}
		switch h.typ {
		case typeStdout:
			if len(stdout)+len(content) > maxBody+maxHeader {
				return Response{}, ErrTooLarge
			}
			stdout = append(stdout, content...)
		case typeStderr:
			if stderr += len(content); stderr > maxStderr {
				return Response{}, ErrTooLarge
			}
		case typeEndRequest:
			if len(content) < 8 {
				return Response{}, ErrProtocol
			}
			if content[4] != requestComplete {
				return Response{}, ErrRejected
			}
			return parseCGI(stdout, maxBody)
		default:
			return Response{}, ErrProtocol
		}
	}
}

// parseCGI splits STDOUT into the header block and the body and reads the
// Status header.
func parseCGI(stdout []byte, maxBody int) (Response, error) {
	end, sepLen := headerEnd(stdout)
	if end < 0 {
		if len(stdout) > maxHeader {
			return Response{}, ErrTooLarge
		}
		return Response{}, ErrProtocol
	}
	if end > maxHeader {
		return Response{}, ErrTooLarge
	}
	status := 200
	for _, line := range strings.Split(string(stdout[:end]), "\n") {
		name, value, ok := strings.Cut(strings.TrimSuffix(line, "\r"), ":")
		if !ok || strings.TrimSpace(name) == "" {
			return Response{}, ErrProtocol
		}
		if !strings.EqualFold(strings.TrimSpace(name), "Status") {
			continue
		}
		code, _, _ := strings.Cut(strings.TrimSpace(value), " ")
		n, err := strconv.Atoi(code)
		if err != nil || len(code) != 3 || n < 100 || n > 599 {
			return Response{}, ErrProtocol
		}
		status = n
	}
	body := stdout[end+sepLen:]
	if len(body) > maxBody {
		return Response{}, ErrTooLarge
	}
	return Response{Status: status, Body: body}, nil
}

// headerEnd is where the header block ends (the first empty line, CRLF or
// LF) and the separator's length, or -1.
func headerEnd(b []byte) (int, int) {
	crlf := bytes.Index(b, []byte("\r\n\r\n"))
	lf := bytes.Index(b, []byte("\n\n"))
	switch {
	case crlf >= 0 && (lf < 0 || crlf < lf):
		return crlf, 4
	case lf >= 0:
		return lf, 2
	}
	return -1, 0
}

// limitReader fails with ErrTooLarge once n bytes were read.
type limitReader struct {
	r io.Reader
	n int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, ErrTooLarge
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}
