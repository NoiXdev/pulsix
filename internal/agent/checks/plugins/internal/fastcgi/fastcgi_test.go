package fastcgi_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/fastcgi"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/fastcgi/fastcgitest"
)

const maxBody = 1 << 20

func get(t *testing.T, s *fastcgitest.Server) (fastcgi.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return fastcgi.Get(ctx, s.Network, s.Address, "/status", "json", maxBody)
}

func mustBody(t *testing.T, resp fastcgi.Response, err error, status int, body string) {
	t.Helper()
	if err != nil || resp.Status != status || string(resp.Body) != body {
		t.Fatalf("Get = %d %q, %v; want %d %q", resp.Status, resp.Body, err, status, body)
	}
}

func mustErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("Get error = %v, want %v", err, want)
	}
}

// TestGetSendsTheFixedRequest: one responder request, no keep-conn, the
// fixed params with the path and query, an empty STDIN.
func TestGetSendsTheFixedRequest(t *testing.T) {
	s := fastcgitest.NewTCP(t, fastcgitest.Respond(200, `{}`))
	resp, err := get(t, s)
	mustBody(t, resp, err, 200, `{}`)
	reqs := s.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[0]
	if r.ID != 1 || r.Role != 1 || r.KeepConn || len(r.Stdin) != 0 {
		t.Fatalf("request = %+v", r)
	}
	want := map[string]string{
		"GATEWAY_INTERFACE": "CGI/1.1",
		"REQUEST_METHOD":    "GET",
		"SCRIPT_FILENAME":   "/",
		"SCRIPT_NAME":       "/status",
		"DOCUMENT_URI":      "/status",
		"REQUEST_URI":       "/status?json",
		"QUERY_STRING":      "json",
		"SERVER_PROTOCOL":   "HTTP/1.1",
	}
	if len(r.Params) != len(want) {
		t.Fatalf("params = %v, want %v", r.Params, want)
	}
	for k, v := range want {
		if r.Params[k] != v {
			t.Fatalf("param %s = %q, want %q (all %v)", k, r.Params[k], v, r.Params)
		}
	}
}

// TestGetLongParamValue: a value of 128 bytes or more takes the four-byte
// length form.
func TestGetLongParamValue(t *testing.T) {
	s := fastcgitest.NewTCP(t, fastcgitest.Respond(200, `{}`))
	path := "/" + strings.Repeat("p", 254)
	resp, err := fastcgi.Get(context.Background(), s.Network, s.Address, path, "json", maxBody)
	mustBody(t, resp, err, 200, `{}`)
	if got := s.Requests()[0].Params["SCRIPT_NAME"]; got != path {
		t.Fatalf("SCRIPT_NAME = %q", got)
	}
}

func TestGetOverUnixSocket(t *testing.T) {
	s := fastcgitest.NewUnix(t, fastcgitest.Respond(0, `{"pool":"www"}`))
	resp, err := get(t, s)
	mustBody(t, resp, err, 200, `{"pool":"www"}`)
}

// TestGetFraming: STDOUT split over many padded records, STDERR records in
// between (dropped), padding on END_REQUEST, LF-only headers.
func TestGetFraming(t *testing.T) {
	body := strings.Repeat("0123456789", 3000)
	cgi := fastcgitest.CGI(200, body)
	var raw bytes.Buffer
	raw.Write(fastcgitest.Record(fastcgitest.TypeStderr, 1, []byte("PHP message: noise"), 6))
	for i := 0; i < len(cgi); i += 7001 {
		raw.Write(fastcgitest.Record(fastcgitest.TypeStdout, 1, []byte(cgi[i:min(i+7001, len(cgi))]), 255))
		raw.Write(fastcgitest.Record(fastcgitest.TypeStderr, 1, []byte("more noise"), 0))
	}
	raw.Write(fastcgitest.Record(fastcgitest.TypeStdout, 1, nil, 0))
	raw.Write(fastcgitest.Record(fastcgitest.TypeEndRequest, 1, []byte{0, 0, 0, 0, 0, 0, 0, 0}, 8))
	s := fastcgitest.NewTCP(t, fastcgitest.Raw(raw.Bytes()))
	resp, err := get(t, s)
	mustBody(t, resp, err, 200, body)

	// A record of the largest size: 65535 content bytes and 255 of padding.
	big := strings.Repeat("y", 65535-len(fastcgitest.CGI(200, "")))
	maxRec := fastcgitest.Record(fastcgitest.TypeStdout, 1, []byte(fastcgitest.CGI(200, big)), 255)
	if len(maxRec) != 8+65535+255 {
		t.Fatalf("record of %d bytes", len(maxRec))
	}
	s = fastcgitest.NewTCP(t, fastcgitest.Raw(append(append(maxRec, fastcgitest.Record(fastcgitest.TypeStdout, 1, nil, 0)...), fastcgitest.End(1, 0)...)))
	resp, err = get(t, s)
	mustBody(t, resp, err, 200, big)

	s.SetHandler(fastcgitest.Raw(append(fastcgitest.Stdout(1, "Status: 404 Not Found\nContent-type: text/html\n\nFile not found.\n", 3), fastcgitest.End(1, 0)...)))
	resp, err = get(t, s)
	mustBody(t, resp, err, 404, "File not found.\n")
}

// TestGetStatusHeader: the Status header's code, case-insensitive name;
// absent means 200.
func TestGetStatusHeader(t *testing.T) {
	for _, tc := range []struct {
		head string
		want int
	}{
		{"Status: 403 Forbidden\r\n", 403},
		{"status:404\r\n", 404},
		{"STATUS: 500\r\n", 500},
		{"X-Powered-By: PHP\r\n", 200},
		{"", 200},
	} {
		s := fastcgitest.NewTCP(t, fastcgitest.Raw(append(fastcgitest.Stdout(1, tc.head+"Content-type: text/plain\r\n\r\nx", 100), fastcgitest.End(1, 0)...)))
		resp, err := get(t, s)
		mustBody(t, resp, err, tc.want, "x")
	}
}

// TestGetMalformed: anything that is not a well-formed responder answer
// is ErrProtocol.
func TestGetMalformed(t *testing.T) {
	ok := fastcgitest.Stdout(1, fastcgitest.CGI(200, "{}"), 100)
	for name, raw := range map[string][]byte{
		"no header end":       append(fastcgitest.Stdout(1, "Content-type: text/plain\r\n{}", 100), fastcgitest.End(1, 0)...),
		"header without ':'":  append(fastcgitest.Stdout(1, "garbage\r\n\r\n{}", 100), fastcgitest.End(1, 0)...),
		"bad status":          append(fastcgitest.Stdout(1, "Status: abc\r\n\r\n{}", 100), fastcgitest.End(1, 0)...),
		"status out of range": append(fastcgitest.Stdout(1, "Status: 99\r\n\r\n{}", 100), fastcgitest.End(1, 0)...),
		"empty stdout":        fastcgitest.End(1, 0),
		"foreign request id":  append(fastcgitest.Stdout(2, fastcgitest.CGI(200, "{}"), 100), fastcgitest.End(2, 0)...),
		"management record":   append(append([]byte{}, ok...), append(fastcgitest.Record(fastcgitest.TypeUnknownType, 0, make([]byte, 8), 0), fastcgitest.End(1, 0)...)...),
		"unknown record type": append(append([]byte{}, ok...), append(fastcgitest.Record(42, 1, []byte("x"), 0), fastcgitest.End(1, 0)...)...),
		"stdin from server":   append(append([]byte{}, ok...), append(fastcgitest.Record(fastcgitest.TypeStdin, 1, []byte("x"), 0), fastcgitest.End(1, 0)...)...),
		"short end request":   append(append([]byte{}, ok...), fastcgitest.Record(fastcgitest.TypeEndRequest, 1, []byte{0, 0}, 0)...),
		"version 2":           {2, 6, 0, 1, 0, 0, 0, 0},
		"http answer":         []byte("HTTP/1.1 400 Bad Request\r\n\r\n"),
	} {
		t.Run(name, func(t *testing.T) {
			s := fastcgitest.NewTCP(t, fastcgitest.Raw(raw))
			_, err := get(t, s)
			mustErr(t, err, fastcgi.ErrProtocol)
		})
	}
}

// TestGetRejected: END_REQUEST with a protocol status other than
// REQUEST_COMPLETE (overloaded, unknown role, cannot multiplex).
func TestGetRejected(t *testing.T) {
	for _, ps := range []byte{1, 2, 3} {
		s := fastcgitest.NewTCP(t, fastcgitest.Raw(fastcgitest.End(1, ps)))
		_, err := get(t, s)
		mustErr(t, err, fastcgi.ErrRejected)
	}
}

// TestGetBounds: a body past maxBody, a header block past its bound,
// STDERR past its bound and an endless stream of empty records are all
// ErrTooLarge, and the client stops reading.
func TestGetBounds(t *testing.T) {
	t.Run("body", func(t *testing.T) {
		s := fastcgitest.NewTCP(t, fastcgitest.Respond(200, strings.Repeat("x", 1001)))
		_, err := fastcgi.Get(context.Background(), s.Network, s.Address, "/status", "json", 1000)
		mustErr(t, err, fastcgi.ErrTooLarge)
		s.SetHandler(fastcgitest.Respond(200, strings.Repeat("x", 1000)))
		resp, err := fastcgi.Get(context.Background(), s.Network, s.Address, "/status", "json", 1000)
		if err != nil || len(resp.Body) != 1000 {
			t.Fatalf("a body of exactly the bound: %d bytes, %v", len(resp.Body), err)
		}
	})
	t.Run("header", func(t *testing.T) {
		head := "X-Big: " + strings.Repeat("h", 20<<10) + "\r\n\r\n{}"
		s := fastcgitest.NewTCP(t, fastcgitest.Raw(append(fastcgitest.Stdout(1, head, 4096), fastcgitest.End(1, 0)...)))
		_, err := get(t, s)
		mustErr(t, err, fastcgi.ErrTooLarge)
	})
	t.Run("header without an end", func(t *testing.T) {
		head := "X-Big: " + strings.Repeat("h", 20<<10)
		s := fastcgitest.NewTCP(t, fastcgitest.Raw(append(fastcgitest.Stdout(1, head, 4096), fastcgitest.End(1, 0)...)))
		_, err := get(t, s)
		mustErr(t, err, fastcgi.ErrTooLarge)
	})
	// STDOUT past body + header bound fails while reading: this stream
	// sends 20 KiB of STDOUT (under the raw bound) and then neither ends
	// nor closes, so only the in-loop cap can answer before the deadline.
	t.Run("stdout while reading", func(t *testing.T) {
		var s *fastcgitest.Server
		s = fastcgitest.NewTCP(t, func(_ fastcgitest.Request, conn net.Conn) {
			_, _ = conn.Write(bytes.Repeat(fastcgitest.Record(fastcgitest.TypeStdout, 1, bytes.Repeat([]byte("z"), 1024), 0), 20))
			<-s.Done()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := fastcgi.Get(ctx, s.Network, s.Address, "/status", "json", 1000)
		mustErr(t, err, fastcgi.ErrTooLarge)
	})
	t.Run("stderr", func(t *testing.T) {
		var raw []byte
		for range 20 {
			raw = append(raw, fastcgitest.Record(fastcgitest.TypeStderr, 1, bytes.Repeat([]byte("e"), 4096), 0)...)
		}
		s := fastcgitest.NewTCP(t, fastcgitest.Raw(append(raw, fastcgitest.End(1, 0)...)))
		_, err := get(t, s)
		mustErr(t, err, fastcgi.ErrTooLarge)
	})
	t.Run("endless empty records", func(t *testing.T) {
		s := fastcgitest.NewTCP(t, func(_ fastcgitest.Request, conn net.Conn) {
			empty := bytes.Repeat(fastcgitest.Record(fastcgitest.TypeStderr, 1, nil, 0), 1024)
			for {
				if _, err := conn.Write(empty); err != nil {
					return
				}
			}
		})
		_, err := fastcgi.Get(context.Background(), s.Network, s.Address, "/status", "json", 1000)
		mustErr(t, err, fastcgi.ErrTooLarge)
	})
}

// TestGetEarlyClose: a connection that ends before END_REQUEST, whatever
// came before, is io.ErrUnexpectedEOF.
func TestGetEarlyClose(t *testing.T) {
	full := append(fastcgitest.Stdout(1, fastcgitest.CGI(200, "{}"), 100), fastcgitest.End(1, 0)...)
	for _, n := range []int{0, 3, 8, 12, len(full) - 1} {
		s := fastcgitest.NewTCP(t, fastcgitest.Raw(full[:n]))
		_, err := get(t, s)
		mustErr(t, err, io.ErrUnexpectedEOF)
	}
}

// TestGetDeadline: a server that never answers ends at the context's
// deadline with a timeout error.
func TestGetDeadline(t *testing.T) {
	s := fastcgitest.NewTCP(t, func(_ fastcgitest.Request, _ net.Conn) {})
	s.SetHandler(func(_ fastcgitest.Request, _ net.Conn) { <-s.Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := fastcgi.Get(ctx, s.Network, s.Address, "/status", "json", maxBody)
	if !errors.Is(err, os.ErrDeadlineExceeded) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Get error = %v, want a deadline", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
}

// TestGetCancel: cancelling the context unblocks a pending read and
// returns the context's error.
func TestGetCancel(t *testing.T) {
	s := fastcgitest.NewTCP(t, func(_ fastcgitest.Request, _ net.Conn) {})
	s.SetHandler(func(_ fastcgitest.Request, _ net.Conn) { <-s.Done() })
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for len(s.Requests()) == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	_, err := fastcgi.Get(ctx, s.Network, s.Address, "/status", "json", maxBody)
	mustErr(t, err, context.Canceled)
}

func TestGetRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	_, err = fastcgi.Get(context.Background(), "tcp", addr, "/status", "json", maxBody)
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "dial" {
		t.Fatalf("Get error = %v, want the dial's error", err)
	}
}

// TestErrorsAreFixedTexts: the client's own errors never carry the
// address, the path or response bytes.
func TestErrorsAreFixedTexts(t *testing.T) {
	for _, err := range []error{fastcgi.ErrProtocol, fastcgi.ErrRejected, fastcgi.ErrTooLarge} {
		if !slices.Contains([]string{"unexpected response format", "request rejected", "response too large"}, err.Error()) {
			t.Fatalf("error text %q", err)
		}
	}
}
