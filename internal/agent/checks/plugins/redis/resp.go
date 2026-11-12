package redis

import (
	"bufio"
	"errors"
	"io"
	"strconv"
	"strings"
)

// The bounds on what a server can make the plugin read or allocate. A
// length announced by the server is checked against maxBulk before any
// buffer is made, and a line cannot outgrow the reader's buffer.
const (
	maxLine = 64 << 10
	maxBulk = 1 << 20
)

// errProtocol is anything the plugin cannot read as the reply it expects:
// garbage, a type it does not use (arrays), a null or oversized bulk string.
var errProtocol = errors.New("unexpected server response")

// serverError is an error reply (-...). Only its class is kept: the text
// can quote the user, a command or a configured value.
type serverError struct{ class string }

func (e *serverError) Error() string { return e.class }

// reply is one decoded RESP2 reply of the kinds INFO and AUTH produce.
type reply struct {
	// kind is '+' (text), ':' (n) or '$' (bulk, or null).
	kind byte
	text string
	n    int64
	bulk string
	null bool
}

// writeCommand sends args as a RESP array of bulk strings: binary-safe, so a
// password with spaces or CR/LF cannot become a second command. Inline
// commands are never used.
func writeCommand(w io.Writer, args ...string) error {
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, a := range args {
		b.WriteString("$" + strconv.Itoa(len(a)) + "\r\n" + a + "\r\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// readLine reads up to and including CRLF and returns the line without it.
// The reader must have been made with at least maxLine bytes of buffer; a
// longer line is a protocol error.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return "", errProtocol
	}
	if err != nil {
		return "", err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return "", errProtocol
	}
	return string(line[:len(line)-2]), nil
}

// classifyError maps an error reply to a fixed class by its prefix alone.
func classifyError(text string) string {
	for _, p := range []string{"NOAUTH", "WRONGPASS", "ERR invalid password", "ERR AUTH", "ERR Client sent AUTH"} {
		if strings.HasPrefix(text, p) {
			return "authentication failed"
		}
	}
	if strings.HasPrefix(text, "NOPERM") {
		return "permission denied"
	}
	return "server error"
}

// readReply reads one reply: a simple string, an error (returned as a
// *serverError), an integer or a bulk string.
func readReply(r *bufio.Reader) (reply, error) {
	line, err := readLine(r)
	if err != nil {
		return reply{}, err
	}
	if line == "" {
		return reply{}, errProtocol
	}
	body := line[1:]
	switch line[0] {
	case '+':
		return reply{kind: '+', text: body}, nil
	case '-':
		return reply{}, &serverError{class: classifyError(body)}
	case ':':
		n, err := strconv.ParseInt(body, 10, 64)
		if err != nil {
			return reply{}, errProtocol
		}
		return reply{kind: ':', n: n}, nil
	case '$':
		n, err := strconv.ParseInt(body, 10, 64)
		if err != nil || n < -1 || n > maxBulk {
			return reply{}, errProtocol
		}
		if n == -1 {
			return reply{kind: '$', null: true}, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return reply{}, err
		}
		if buf[n] != '\r' || buf[n+1] != '\n' {
			return reply{}, errProtocol
		}
		return reply{kind: '$', bulk: string(buf[:n])}, nil
	}
	return reply{}, errProtocol
}
